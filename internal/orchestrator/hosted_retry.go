package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HostedRestartBudget is how long a hosted request waits out a daemon that
// is not answering. The supervisor restarts a daemon that exits within its
// probe interval and the successor rebinds api.sock, so a run only has to
// outlast that gap; past the budget the run fails naming the daemon rather
// than waiting on a machine that has lost one.
const HostedRestartBudget = 20 * time.Second

const (
	hostedRetryFirstWait = 100 * time.Millisecond
	hostedRetryMaxWait   = 2 * time.Second
)

type hostedRetryPolicy int

// safety: unsent is the default because repeating a request the daemon did
// serve would append a second row; repeatable writes a value rather than an
// increment; create names a row the caller identifies, so a conflict on a
// later attempt means the first attempt landed.
const (
	hostedRetryUnsent hostedRetryPolicy = iota
	hostedRetryRepeatable
	hostedRetryCreate
)

// safety: keyed by the pattern the controller registers and matched through a
// mux, because a policy belongs to a route: matching by path segment let a
// node named for a keyword borrow that keyword's policy. Creates carry the
// caller's ids; the rest write a value, or a claim the store upserts by key.
var hostedRoutePolicies = map[string]hostedRetryPolicy{
	"POST /api/v1/runs":            hostedRetryCreate,
	"POST /api/v1/runs/{id}/nodes": hostedRetryCreate,

	"POST /api/v1/runs/{id}/cancel":                           hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/finish":                           hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/heartbeat":                        hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/plan":                             hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/activity":          hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest": hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/deps":              hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/finish":            hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat":         hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/mark-ready":        hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/release":           hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/revoke-ready":      hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/start":             hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/status":            hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/summary":           hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/touch":             hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish":      hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip":        hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start":       hostedRetryRepeatable,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary":     hostedRetryRepeatable,
	"POST /api/v1/maintenance/reconcile-orphans":              hostedRetryRepeatable,
	"POST /api/v1/triggers/{id}/claim":                        hostedRetryRepeatable,
	"POST /api/v1/triggers/{id}/done":                         hostedRetryRepeatable,
	"POST /api/v1/triggers/{id}/heartbeat":                    hostedRetryRepeatable,

	"POST /api/v1/concurrency/{key}/acquire":       hostedRetryRepeatable,
	"POST /api/v1/concurrency/{key}/cancel-waiter": hostedRetryRepeatable,
	"POST /api/v1/concurrency/{key}/force-release": hostedRetryRepeatable,
	"POST /api/v1/concurrency/{key}/heartbeat":     hostedRetryRepeatable,
	"POST /api/v1/concurrency/{key}/release":       hostedRetryRepeatable,
}

var hostedRouteMux = sync.OnceValue(func() *http.ServeMux {
	mux := http.NewServeMux()
	marker := http.NotFoundHandler()
	for pattern := range hostedRoutePolicies {
		mux.Handle(pattern, marker)
	}
	return mux
})

func hostedRetryPolicyFor(req *http.Request) hostedRetryPolicy {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return hostedRetryRepeatable
	case http.MethodPost:
	default:
		return hostedRetryUnsent
	}
	if _, pattern := hostedRouteMux().Handler(req); pattern != "" {
		return hostedRoutePolicies[pattern]
	}
	return hostedRetryUnsent
}

type hostedRetryTransport struct {
	base   http.RoundTripper
	budget time.Duration
	now    func() time.Time
}

func newHostedRetryTransport(base http.RoundTripper) *hostedRetryTransport {
	return &hostedRetryTransport{base: base, budget: HostedRestartBudget, now: time.Now}
}

func (t *hostedRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	policy := hostedRetryPolicyFor(req)
	deadline := t.now().Add(t.budget)
	wait := hostedRetryFirstWait
	for attempt := 1; ; attempt++ {
		next, rewindErr := hostedRewind(req)
		if rewindErr != nil {
			// safety: a body this transport cannot replay is sent once, which
			// is what happens today without any retry at all.
			return t.base.RoundTrip(req)
		}
		resp, err := t.base.RoundTrip(next)
		if err == nil && resp.StatusCode != http.StatusServiceUnavailable {
			return hostedSettleCreate(policy, attempt, resp), nil
		}
		if !hostedRetryAllowed(policy, err) {
			return hostedFinalAnswer(resp), err
		}
		if !t.now().Before(deadline) {
			return hostedFinalAnswer(resp), err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		if waitErr := hostedWait(req.Context(), wait); waitErr != nil {
			return nil, waitErr
		}
		if wait *= 2; wait > hostedRetryMaxWait {
			wait = hostedRetryMaxWait
		}
	}
}

// safety: this transport owns the 503 policy for a hosted request, so the
// answer it gives up on carries no invitation back; otherwise the client's
// own retry loop would spend a second budget on the same wedged daemon.
func hostedFinalAnswer(resp *http.Response) *http.Response {
	if resp != nil && resp.StatusCode == http.StatusServiceUnavailable {
		resp.Header.Del("Retry-After")
	}
	return resp
}

// safety: the daemon writes its 503 before any handler runs, so an answered
// 503 proves the write did not land and every policy but the unsent one may
// repeat it. A transport error proves nothing unless it is a dial failure.
func hostedRetryAllowed(policy hostedRetryPolicy, err error) bool {
	if err != nil && hostedNeverSent(err) {
		return true
	}
	return policy == hostedRetryRepeatable || policy == hostedRetryCreate
}

func hostedNeverSent(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

const hostedConflictBodyCap = 8 << 10

// safety: only a repeat can see this conflict, because the caller names the
// row: the first attempt was cut off before its answer arrived and the row it
// wrote is the one the retry collides with, so the collision is the success
// the caller never got to read.
func hostedSettleCreate(policy hostedRetryPolicy, attempt int, resp *http.Response) *http.Response {
	if policy != hostedRetryCreate || attempt == 1 || !hostedConflictStatus(resp.StatusCode) {
		return resp
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, hostedConflictBodyCap))
	_ = resp.Body.Close()
	if err != nil || !hostedRowAlreadyExists(body) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	resp.StatusCode = http.StatusCreated
	resp.Status = "201 Created"
	resp.Body = io.NopCloser(bytes.NewReader(nil))
	resp.ContentLength = 0
	return resp
}

func hostedConflictStatus(code int) bool {
	return code == http.StatusConflict || code == http.StatusInternalServerError
}

func hostedRowAlreadyExists(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "unique constraint")
}

func hostedRewind(req *http.Request) (*http.Request, error) {
	next := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return next, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("hosted retry: request body cannot be replayed")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	next.Body = body
	return next, nil
}

func hostedWait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
