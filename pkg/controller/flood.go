package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// FloodPolicy bounds how many runs a burst of webhook deliveries or API
// submissions can create on one controller. The zero policy holds nothing
// back, which is the behavior of a controller that was never given one.
//
// A refused submission is always answered: the cap answers 429 and the
// queue-depth shed answers 503, both with Retry-After, and the controller logs
// the principal and the reason at warn. Nothing is dropped silently.
type FloodPolicy struct {
	// RunsPerPrincipalHour caps the runs one principal may create in a rolling
	// hour. Zero is unlimited. Each run a GitHub App delivery creates counts
	// against the team the installation is bound to, because a delivery has
	// no principal of its own.
	RunsPerPrincipalHour int

	// ShedQueueDepth is the pending-trigger depth of one team past which that
	// team's new submissions are shed rather than queued. Zero never sheds.
	ShedQueueDepth int

	// DedupeWindow is how long a content-identical submission answers with the
	// run the first one started instead of starting a second. Zero dedupes
	// nothing. GitHub deliveries carry a delivery id and a body digest and are
	// deduped by the store regardless of this window.
	DedupeWindow time.Duration
}

// WithFloodPolicy installs p as this controller's trigger flood control.
// Calling it with the zero policy restores the unbounded behavior.
func (s *Server) WithFloodPolicy(p FloodPolicy) *Server {
	s.flood = newFloodControl(p)
	return s
}

// safety: a Retry-After shorter than the real refill would invite a caller
// back before a token exists, so sub-second refills round up and nothing
// rounds down.
const (
	minFloodRetryAfter = time.Second
	shedRetryAfter     = 5 * time.Second
	depthWarnInterval  = time.Minute
)

const maxDedupeEntries = 20000

type floodControl struct {
	policy    FloodPolicy
	runs      *ratelimit.Limiter
	dedupe    *dedupeWindow
	depth     *queueDepthCache
	depthWarn *warnEvery
}

func newFloodControl(p FloodPolicy) *floodControl {
	f := &floodControl{policy: p, depthWarn: &warnEvery{every: depthWarnInterval}}
	if p.RunsPerPrincipalHour > 0 {
		f.runs = ratelimit.New(p.RunsPerPrincipalHour, time.Hour)
	}
	if p.DedupeWindow > 0 {
		f.dedupe = &dedupeWindow{window: p.DedupeWindow, seen: make(map[string]dedupeEntry)}
	}
	if p.ShedQueueDepth > 0 {
		f.depth = &queueDepthCache{teams: make(map[store.Team]queueDepth)}
	}
	return f
}

type floodRefusal struct {
	status int
	wait   time.Duration
	msg    string
}

func (f *floodRefusal) write(w http.ResponseWriter) {
	if f.status == http.StatusServiceUnavailable {
		writeRetryAfterStatus(w, f.status, f.wait, f.msg)
		return
	}
	writeRetryAfter(w, f.wait, f.msg)
}

func (s *Server) triggerFloodRefusal(ctx context.Context, tenant *store.Tenant, key, source string) *floodRefusal {
	return s.floodRefusal(tenant.Team(), key, source, func() (int, error) { return s.flood.depth.read(ctx, tenant, time.Now()) })
}

func (s *Server) floodRefusal(team store.Team, key, source string, readDepth func() (int, error)) *floodRefusal {
	if refusal := s.depthRefusal(team, key, source, readDepth); refusal != nil {
		return refusal
	}
	return s.runCapRefusal(key, source)
}

func (s *Server) depthRefusal(team store.Team, key, source string, readDepth func() (int, error)) *floodRefusal {
	f := s.flood
	if f == nil {
		return nil
	}
	if f.depth != nil {
		depth, err := readDepth()
		if err != nil {
			// safety: a depth this controller cannot read is not grounds to
			// refuse work, so the submission proceeds and the cap still binds.
			if f.depthWarn.due() {
				s.logger.Warn("trigger flood control: queue depth unavailable",
					"principal", key, "source", source, "err", err)
			}
		} else if depth >= f.policy.ShedQueueDepth {
			s.logger.Warn("trigger shed",
				"principal", key, "team", team, "source", source,
				"reason", "team queue depth above the shed threshold",
				"queue_depth", depth, "threshold", f.policy.ShedQueueDepth)
			return &floodRefusal{
				status: http.StatusServiceUnavailable, wait: shedRetryAfter,
				msg: "this team's queue is above its shed threshold",
			}
		}
	}
	return nil
}

func (s *Server) runCapRefusal(key, source string) *floodRefusal {
	f := s.flood
	if f == nil {
		return nil
	}
	now := time.Now()
	if f.runs != nil {
		allowed, wait := f.runs.AllowWithRetry(key, now)
		if !allowed {
			wait = max(wait, minFloodRetryAfter)
			s.logger.Warn("trigger shed",
				"principal", key, "source", source, "reason", "hourly run cap reached",
				"cap_per_hour", f.policy.RunsPerPrincipalHour, "retry_after", wait)
			return &floodRefusal{
				status: http.StatusTooManyRequests, wait: wait,
				msg: "hourly run cap reached for this principal",
			}
		}
	}
	return nil
}

type dedupeEntry struct {
	key string
	at  time.Time
}

// safety: the answer expires rather than persisting, because two identical
// submissions an hour apart are two runs and only a redelivery is one.
type dedupeWindow struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]dedupeEntry
}

// safety: looking up and recording under one lock is what stops two simultaneous
// identical submissions from both starting a run.
func (d *dedupeWindow) claim(digest, key string, now time.Time) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.seen[digest]; ok && now.Sub(e.at) < d.window {
		return e.key, true
	}
	d.evictLocked(now)
	d.seen[digest] = dedupeEntry{key: key, at: now}
	return "", false
}

// safety: the window bounds this map in time but not under a flood, so a full
// map drops its expired entries first and its coldest half only if that left it full.
func (d *dedupeWindow) evictLocked(now time.Time) {
	if len(d.seen) < maxDedupeEntries {
		return
	}
	for k, e := range d.seen {
		if now.Sub(e.at) >= d.window {
			delete(d.seen, k)
		}
	}
	if len(d.seen) < maxDedupeEntries {
		return
	}
	ages := make([]time.Time, 0, len(d.seen))
	for _, e := range d.seen {
		ages = append(ages, e.at)
	}
	slices.SortFunc(ages, func(a, b time.Time) int { return a.Compare(b) })
	cutoff := ages[len(ages)/2]
	for k, e := range d.seen {
		if !e.at.After(cutoff) {
			delete(d.seen, k)
		}
	}
}

// perf: a flood asks for the depth far faster than it changes, and each answer
// is a scan, so one read serves every submission of a team in the same second.
const queueDepthTTL = time.Second

// safety: the count must not ride one requester's context, or a client that
// hangs up decides what every other submission sees.
const queueDepthTimeout = 2 * time.Second

// safety: keyed by team, so one team's backlog never sheds another team's
// submissions. The map holds one entry per team that submitted, which the
// team count bounds.
type queueDepthCache struct {
	mu    sync.Mutex
	teams map[store.Team]queueDepth
	// perf: a burst misses the cache together, so one count serves every
	// submission of a team that arrives while it runs.
	counts singleflight.Group
}

type queueDepth struct {
	depth int
	at    time.Time
}

// safety: the depth is read from one team's queue ([store.Tenant] is one), so
// one team's backlog never sheds another team's submissions.
type pendingTriggerCounter interface {
	Team() store.Team
	CountPendingTriggers(ctx context.Context) (int, error)
}

func (q *queueDepthCache) read(parent context.Context, tenant pendingTriggerCounter, now time.Time) (int, error) {
	team := tenant.Team()
	q.mu.Lock()
	cached, ok := q.teams[team]
	q.mu.Unlock()
	if ok && now.Sub(cached.at) < queueDepthTTL {
		return cached.depth, nil
	}
	counted, err, _ := q.counts.Do(string(team), func() (any, error) {
		// safety: the count outlives one requester, so the caller's cancel is
		// dropped while its values and tracing are kept.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), queueDepthTimeout)
		defer cancel()
		n, err := tenant.CountPendingTriggers(ctx)
		if err != nil {
			return 0, err
		}
		q.mu.Lock()
		q.teams[team] = queueDepth{depth: n, at: now}
		q.mu.Unlock()
		return n, nil
	})
	if err != nil {
		return 0, err
	}
	return counted.(int), nil
}

// safety: a depth that cannot be read fails on every submission of a flood,
// so the line that says so is worth one a window rather than one a request.
type warnEvery struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
}

func (w *warnEvery) due() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if !w.last.IsZero() && now.Sub(w.last) < w.every {
		return false
	}
	w.last = now
	return true
}

type intakeRunToken struct {
	key string
}

func (e *intakeRunToken) Error() string { return "intake requires a run-cap token" }

type intakeRunTokenKey struct{}

func (s *Server) admitWithFlood(ctx context.Context, admit func(context.Context) (controllerwire.CreateRunResponse, error)) (controllerwire.CreateRunResponse, error) {
	response, err := admit(ctx)
	var pending *intakeRunToken
	if !errors.As(err, &pending) {
		return response, err
	}
	if err := floodRefusalError(s.runCapRefusal(pending.key, "api")); err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	return admit(context.WithValue(ctx, intakeRunTokenKey{}, pending.key))
}

func (s *Server) admissionFloodRefusal(ctx context.Context, resolver capability.AuthorityResolver, request store.IntakeRequest, p capability.Principal) error {
	if s.flood == nil || request.Authority.Kind == "schedule" || request.VerifiedDeliveryID != "" || request.Request.Lineage.ParentRunID != "" {
		return nil
	}
	key := string(p.Authority.Kind) + ":" + p.Authority.ID
	if s.flood.depth != nil {
		team := store.Team(p.Capability.Team)
		if err := floodRefusalError(s.depthRefusal(team, key, "api", func() (int, error) { return store.IntakeQueueDepth(ctx, resolver, team) })); err != nil {
			return err
		}
	}
	if s.flood.runs != nil && ctx.Value(intakeRunTokenKey{}) != key {
		return &intakeRunToken{key: key}
	}
	return nil
}

func floodRefusalError(refusal *floodRefusal) error {
	if refusal == nil {
		return nil
	}
	code := apierr.RateLimited
	if refusal.status == http.StatusServiceUnavailable {
		code = apierr.Unavailable
	}
	return &apierr.Error{Code: code, Message: refusal.msg, Details: map[string]any{"retry_after_ms": refusal.wait.Milliseconds()}}
}

func (s *Server) dedupeIntake(a capability.Authorized, body *controllerwire.CreateRunRequest) error {
	if s.flood == nil || s.flood.dedupe == nil || body.IdempotencyKey != "" {
		return nil
	}
	intake, err := IntakeForStore(a, *body)
	if err != nil {
		return err
	}
	canonical, err := json.Marshal(intake.Request)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	key := hex.EncodeToString(nonce[:])
	if existing, found := s.flood.dedupe.claim(hex.EncodeToString(digest[:]), key, time.Now()); found {
		key = existing
	}
	body.IdempotencyKey = key
	return nil
}
