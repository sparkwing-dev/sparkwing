package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type concurrencyBoundary struct {
	*credentialBoundary
	group, holder string
}

func (b concurrencyBoundary) CheckBinding(ctx context.Context, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	if err := b.credentialBoundary.CheckBinding(ctx, p, rule, target); err != nil {
		return err
	}
	if rule == capability.BindSlotOfBoundNode && (target.SlotKey != b.group || target.SlotHolderID != "" && target.SlotHolderID != b.holder) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "undeclared slot", Details: map[string]any{"reason": apierr.ReasonBinding}}
	}
	return nil
}

type concurrencyTransactions struct {
	team    store.Team
	calls   int
	auth    capability.Authorized
	acquire store.AcquireSlotRequest
	err     error
	kind    store.AcquireKind
	group   string
}

func (f *concurrencyTransactions) Team() store.Team { return f.team }
func (f *concurrencyTransactions) entered(a capability.Authorized) error {
	f.calls++
	f.auth = a
	return f.err
}
func (f *concurrencyTransactions) AcquireAuthorizedSlot(_ context.Context, a capability.Authorized, in store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	f.acquire = in
	return store.AcquireSlotResponse{Kind: f.kind, HolderID: in.HolderID, LeaseExpiresAt: time.Date(2026, 10, 14, 12, 1, 0, 0, time.UTC)}, f.entered(a)
}
func (f *concurrencyTransactions) HeartbeatAuthorizedSlot(_ context.Context, a capability.Authorized, _, _ string, _ time.Duration) (time.Time, bool, error) {
	return time.Date(2026, 10, 14, 12, 1, 0, 0, time.UTC), true, f.entered(a)
}
func (f *concurrencyTransactions) ReadAuthorizedHolder(_ context.Context, a capability.Authorized, _, id string) (store.ConcurrencyHolder, error) {
	return store.ConcurrencyHolder{HolderID: id, RunID: a.Target.SlotRunID, NodeID: a.Target.SlotNodeID, Cost: 1}, f.entered(a)
}
func (f *concurrencyTransactions) ReleaseAuthorizedSlot(_ context.Context, a capability.Authorized, _, _, _, _, _ string, _ time.Duration) error {
	return f.entered(a)
}
func (f *concurrencyTransactions) ReadAuthorizedGroup(_ context.Context, a capability.Authorized, group string) (*store.ConcurrencyState, error) {
	if f.group != "" {
		group = f.group
	}
	return &store.ConcurrencyState{Key: group, Capacity: 2, EffectiveCapacity: 1, UsedCost: 1,
		Holders: []store.ConcurrencyHolder{{HolderID: "other-holder", RunID: "run", NodeID: "sibling", Superseded: true, Cost: 1, ClaimedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), LeaseExpiresAt: time.Date(2026, 10, 14, 12, 1, 0, 0, time.UTC)}},
		Waiters: []store.ConcurrencyWaiter{{RunID: "next", NodeID: "next-job", Position: 0, Policy: "queue", ArrivedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), CancelTimeout: 3 * time.Second}},
	}, f.entered(a)
}
func (f *concurrencyTransactions) ResolveAuthorizedWaiter(_ context.Context, a capability.Authorized, _, _, _, _, _, _ string, _ bool) (store.WaiterResolution, error) {
	return store.WaiterResolution{Status: store.WaiterStillWaiting, LeaderRunID: "leader", LeaderNodeID: "leader-job"}, f.entered(a)
}
func (f *concurrencyTransactions) CancelAuthorizedWaiter(_ context.Context, a capability.Authorized, _ string, _ store.NodeRef) (bool, error) {
	return true, f.entered(a)
}
func (f *concurrencyTransactions) ForceReleaseAuthorizedSlots(_ context.Context, a capability.Authorized, _ string) ([]store.ConcurrencyHolder, error) {
	return []store.ConcurrencyHolder{{HolderID: "expired", RunID: "run", NodeID: "job"}}, f.entered(a)
}

func concurrencyServer(b *credentialBoundary, f *concurrencyTransactions, group, holder string) http.Handler {
	auth := consoleAuthorization(b)
	auth.Bindings = concurrencyBoundary{b, group, holder}
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(auth).
		WithConcurrencyStore(func(_ context.Context, _ store.Team) (controller.ConcurrencyStore, error) { return f, nil }).Handler()
}

func concurrencyRequest(h http.Handler, b *credentialBoundary, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if b.raw != "" {
		r.Header.Set("Authorization", "Bearer "+b.raw)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Sparkwing-Generation", "999")
	r.Header.Set("X-Sparkwing-Holder", "foreign")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestConcurrencyHTTPTransactions(t *testing.T) {
	for _, owner := range []string{"job", "trigger", "plan"} {
		for _, op := range []string{"acquire", "heartbeat", "release", "cancel-waiter", "holder", "resolve", "group", "force-release"} {
			t.Run(owner+"/"+op, func(t *testing.T) {
				b := newCredentialBoundary()
				if owner == "trigger" {
					b = ownerBoundary()
				} else if owner == "plan" {
					b = planBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.RunsState)
				}
				binding := b.snapshot.Authorities[0].Principal.Capability.Binding
				f := &concurrencyTransactions{team: "team", kind: store.AcquireGranted}
				method, path, body := "POST", "/api/v1/queue/groups/deploy/"+op, `{"holder_id":"holder"}`
				switch op {
				case "acquire":
					body = fmt.Sprintf(`{"holder_id":"holder","run_id":"run","job_id":%q,"max":2,"cost":1,"policy":"queue","lease_secs":60}`, binding.NodeID)
				case "release":
					body = `{"holder_id":"holder","outcome":"cancelled"}`
				case "cancel-waiter":
					body = fmt.Sprintf(`{"run_id":"run","job_id":%q}`, binding.NodeID)
				case "holder":
					method, path = "GET", path+"?holder_id=holder"
				case "resolve":
					method, path = "GET", path+"?run_id=run&job_id="+binding.NodeID
				case "group":
					method, path = "GET", "/api/v1/queue/groups/deploy"
				case "force-release":
					b = intakeBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl}
				}
				w := concurrencyRequest(concurrencyServer(b, f, "deploy", "holder"), b, method, path, body)
				want := 200
				if op == "release" {
					want = 204
				}
				if w.Code != want || f.calls != 1 || f.auth.Target.Team != "team" || f.auth.Target.SlotKey != "deploy" || f.auth.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority {
					t.Fatalf("HTTP %d %s calls=%d auth=%+v", w.Code, w.Body, f.calls, f.auth)
				}
				if op != "force-release" && (f.auth.Target.SlotGeneration != binding.Generation || f.auth.Target.SlotRunID != binding.RunID || f.auth.Target.SlotNodeID != binding.NodeID || f.auth.Binding != binding) {
					t.Fatalf("request replaced binding: %+v", f.auth)
				}
				if strings.Contains(w.Body.String(), "node_id") {
					t.Fatalf("old vocabulary escaped: %s", w.Body)
				}
				if op == "acquire" && (f.acquire.NodeID != binding.NodeID || f.acquire.Lease != time.Minute) {
					t.Fatal("acquire request changed")
				}
				if op == "group" {
					var result controllerwire.QueueGroup
					if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Group != "deploy" || len(result.Waiters) != 1 || result.Waiters[0].Position != 1 || result.Waiters[0].CancelTimeout != 3*time.Second || result.Holders[0].JobID != "sibling" || !result.Holders[0].Superseded {
						t.Fatalf("group=%+v", result)
					}
				}
			})
		}
	}
}

func TestConcurrencyHTTPBindingProperty(t *testing.T) {
	check := func(seed [16]byte, which uint8) bool {
		b := newCredentialBoundary()
		group := fmt.Sprintf("deploy/%x +", seed)
		holder := "own-holder"
		run, job := "run", "job"
		key := group
		switch which % 4 {
		case 0:
			run = "other-run"
		case 1:
			job = "sibling"
		case 2:
			key = "other-" + group
		case 3:
			holder = "other-holder"
		}
		for _, op := range []string{"acquire", "heartbeat", "release", "cancel-waiter", "holder", "resolve", "group"} {
			f := &concurrencyTransactions{team: "team", kind: store.AcquireGranted}
			method, path, body := "POST", "/api/v1/queue/groups/"+url.PathEscape(key)+"/"+op, ""
			switch op {
			case "acquire":
				body = fmt.Sprintf(`{"holder_id":%q,"run_id":%q,"job_id":%q}`, holder, run, job)
			case "heartbeat", "release":
				body = fmt.Sprintf(`{"holder_id":%q}`, holder)
			case "cancel-waiter":
				body = fmt.Sprintf(`{"run_id":%q,"job_id":%q}`, run, job)
			case "holder":
				method, path = "GET", path+"?holder_id="+url.QueryEscape(holder)
			case "resolve":
				method, path = "GET", path+"?run_id="+run+"&job_id="+job
			case "group":
				method, path = "GET", "/api/v1/queue/groups/"+url.PathEscape(key)
			}
			// safety: each operation varies only selectors it actually accepts.
			if which%4 == 0 || which%4 == 1 {
				if op != "acquire" && op != "cancel-waiter" && op != "resolve" {
					continue
				}
			}
			if which%4 == 3 && op != "acquire" && op != "heartbeat" && op != "release" && op != "holder" {
				continue
			}
			w := concurrencyRequest(concurrencyServer(b, f, group, "own-holder"), b, method, path, body)
			if w.Code != 403 || f.calls != 0 || !strings.Contains(w.Body.String(), `"reason"`) {
				t.Logf("%d %s op=%s calls=%d", w.Code, w.Body, op, f.calls)
				return false
			}
		}
		return true
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencyHTTPRefusals(t *testing.T) {
	for _, scenario := range []string{"missing", "revoked", "expired", "membership", "machine", "deployment", "missing grant", "bootstrap", "cancel acquire", "cancel release", "stale generation", "transaction race", "private error", "foreign tenant", "wrong group", "nil adapter", "bad body", "old field", "trailing body", "negative lease", "overflow lease", "duplicate selector", "old query", "head"} {
		t.Run(scenario, func(t *testing.T) {
			b := newCredentialBoundary()
			f := &concurrencyTransactions{team: "team", kind: store.AcquireGranted}
			method, path, body := "POST", "/api/v1/queue/groups/deploy/acquire", `{"holder_id":"holder","run_id":"run","job_id":"job"}`
			status, code, calls := 401, apierr.Unauthenticated, 0
			p := &b.snapshot.Authorities[0].Principal
			switch scenario {
			case "missing":
				b.raw = ""
			case "revoked":
				p.Capability.RevokedAt = &b.snapshot.Now
			case "expired":
				p.Capability.ExpiresAt = &b.snapshot.Now
			case "membership":
				b.snapshot.Authorities[0].MembershipValid = false
			case "machine", "deployment":
				b = intakeBoundary()
				p = &b.snapshot.Authorities[0].Principal
				p.Capability.Subject.Kind = capability.SubjectMachine
				if scenario == "deployment" {
					p.Capability.Subject.Kind = capability.SubjectOperator
				}
				p.Capability.Grants = []capability.Grant{capability.NodeReport, capability.RunsState}
				status, code = 403, apierr.Forbidden
			case "missing grant":
				p.Capability.Grants = nil
				status, code = 403, apierr.Forbidden
			case "bootstrap":
				p.Capability.Delivery = capability.DeliveryBootstrap
				p.Capability.Grants = []capability.Grant{capability.SourceRead, capability.ClaimExchange}
				status, code = 403, apierr.Forbidden
			case "cancel acquire":
				b.snapshot.Binding.RunCancelled = true
			case "cancel release":
				b.snapshot.Binding.RunCancelled = true
				path, body = "/api/v1/queue/groups/deploy/release", `{"holder_id":"holder"}`
				status, code, calls = 204, "", 1
			case "stale generation":
				b.snapshot.Binding.Current.Generation++
			case "transaction race":
				f.err = store.ErrLockHeld
				status, code, calls = 409, apierr.LockHeld, 1
			case "private error":
				f.err = errors.New("private credential diagnostic")
				status, code, calls = 500, apierr.Internal, 1
			case "foreign tenant":
				f.team = "foreign"
				status, code = 500, apierr.PolicyMissing
			case "wrong group":
				path, method = "/api/v1/queue/groups/deploy", "GET"
				f.group = "other"
				status, code, calls = 500, apierr.PolicyMissing, 1
			case "nil adapter":
				status, code = 500, apierr.PolicyMissing
			case "bad body":
				body = "{"
				status, code = 400, apierr.BadRequest
			case "old field":
				body = `{"run_id":"run","node_id":"job","holder_id":"holder"}`
				status, code = 400, apierr.BadRequest
			case "trailing body":
				body += " {}"
				status, code = 400, apierr.BadRequest
			case "negative lease":
				body = `{"run_id":"run","job_id":"job","holder_id":"holder","lease_secs":-1}`
				status, code = 400, apierr.BadRequest
			case "overflow lease":
				body = `{"run_id":"run","job_id":"job","holder_id":"holder","lease_secs":9223372036854775807}`
				status, code = 400, apierr.BadRequest
			case "duplicate selector":
				path, method = "/api/v1/queue/groups/deploy/holder?holder_id=holder&holder_id=foreign", "GET"
				status, code = 400, apierr.BadRequest
			case "old query":
				path, method = "/api/v1/queue/groups/deploy/resolve?run_id=run&node_id=job", "GET"
				status, code = 400, apierr.BadRequest
			case "head":
				path, method = "/api/v1/queue/groups/deploy", "HEAD"
				status, code = 405, apierr.MethodNotAllowed
			}
			h := concurrencyServer(b, f, "deploy", "holder")
			if scenario == "nil adapter" {
				h = controller.New(nil, nil).WithAuthorization(consoleAuthorization(b)).Handler()
			}
			w := concurrencyRequest(h, b, method, path, body)
			if code != "" {
				assertEngineError(t, w, status, code)
			} else if w.Code != status {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			if f.calls != calls || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("calls=%d want=%d body=%s", f.calls, calls, w.Body)
			}
		})
	}
}

func TestConcurrencyHTTPClientUsesGroupRoutes(t *testing.T) {
	b := newCredentialBoundary()
	f := &concurrencyTransactions{team: "team", kind: store.AcquireGranted}
	c := client.NewWithToken("http://controller.invalid", &http.Client{Transport: reportTransport{concurrencyServer(b, f, "deploy/prod +", "holder")}}, b.raw)
	ctx := t.Context()
	key := "deploy/prod +"
	if result, err := c.AcquireSlot(ctx, key, client.AcquireSlotRequest{HolderID: "holder", RunID: "run", NodeID: "job", Max: 2, Cost: 1, Policy: "queue"}); err != nil || !result.Granted {
		t.Fatalf("acquire=%+v %v", result, err)
	}
	if _, err := c.HeartbeatSlot(ctx, key, "holder", time.Minute); err != nil {
		t.Fatal(err)
	}
	if result, err := c.ObserveSlot(ctx, key, "holder"); err != nil || result.NodeID != "job" {
		t.Fatalf("holder=%+v %v", result, err)
	}
	if result, err := c.ResolveWaiter(ctx, key, "run", "job", "", "leader", "leader-job", false); err != nil || result.LeaderNodeID != "leader-job" {
		t.Fatalf("resolution=%+v %v", result, err)
	}
	if result, err := c.ConcurrencyState(ctx, key); err != nil || result.Group != key || result.Waiters[0].CancelTimeout != 3*time.Second {
		t.Fatalf("group=%+v %v", result, err)
	}
	if ok, err := c.CancelWaiter(ctx, key, "run", "job"); err != nil || !ok {
		t.Fatal(err)
	}
	if err := c.ReleaseSlot(ctx, key, "holder", "success", "", "", 0); err != nil {
		t.Fatal(err)
	}
	b.snapshot.Authorities = intakeBoundary().snapshot.Authorities
	b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl}
	if dropped, err := c.ForceReleaseSuperseded(ctx, key); err != nil || len(dropped) != 1 {
		t.Fatalf("drop=%+v %v", dropped, err)
	}
	if f.calls != 8 {
		t.Fatalf("transactions=%d", f.calls)
	}
}

func TestConcurrencyHTTPRetiredRoutes(t *testing.T) {
	b := newCredentialBoundary()
	f := &concurrencyTransactions{team: "team"}
	h := concurrencyServer(b, f, "deploy", "holder")
	for _, method := range []string{"GET", "POST"} {
		for _, op := range []string{"acquire", "heartbeat", "release", "holder", "state", "resolve", "cancel-waiter", "force-release"} {
			if w := concurrencyRequest(h, b, method, "/api/v1/concurrency/deploy/"+op, `{}`); w.Code != 404 || f.calls != 0 {
				t.Fatalf("retired %s %s HTTP %d %s", method, op, w.Code, w.Body)
			}
		}
	}
}

var _ controller.ConcurrencyStore = (*concurrencyTransactions)(nil)
