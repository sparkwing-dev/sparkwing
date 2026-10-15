package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type snapshotTransactions struct {
	engineTransactions
	snapshot           store.RunSnapshot
	graph              store.ScheduleSnapshot
	reads, projections int
	projectionErr      error
}

func (f *snapshotTransactions) ReadSnapshot(_ context.Context, run string) (store.RunSnapshot, error) {
	f.reads++
	if run != "run" {
		return store.RunSnapshot{}, store.ErrNotFound
	}
	return f.snapshot, f.readErr
}

func snapshotFixture(b *credentialBoundary) *snapshotTransactions {
	return &snapshotTransactions{
		snapshot: store.RunSnapshot{
			Run: store.RunRow{ID: "run", Team: "team"}, StoreNow: b.snapshot.Now,
			Nodes: []store.NodeRow{
				{Ref: store.NodeRef{RunID: "run", NodeID: "first"}, Status: "pending", Generation: 2, Spec: json.RawMessage(`{"id":"first"}`)},
				{Ref: store.NodeRef{RunID: "run", NodeID: "generated"}, Status: "running", Generation: 4, Attempt: 2, StateSeq: 9, Spec: json.RawMessage(`{"id":"generated","deps":["first"]}`), CapabilityPrefix: "private-prefix", ClaimedBy: "private-holder"},
			},
		},
		graph: store.ScheduleSnapshot{
			Nodes:      []store.ScheduleNode{{ID: "first", Ready: true}, {ID: "generated", Claimed: true}},
			Expansions: []store.ScheduleExpansion{{GeneratorID: "empty", SourceNodeID: "first", State: "accepted"}, {GeneratorID: "group", SourceNodeID: "first", State: "accepted", Members: []string{"generated"}}},
		},
	}
}

func snapshotServer(t *testing.T, b *credentialBoundary, f *snapshotTransactions) http.Handler {
	t.Helper()
	e := fakeEngine(t, &f.engineTransactions)
	e.ResolveTenant = func(_ context.Context, team store.Team) (controller.RunTransactions, error) {
		if team != "team" {
			t.Fatalf("tenant = %q", team)
		}
		return f, nil
	}
	e.ProjectScheduling = func(snapshot store.RunSnapshot) (store.ScheduleSnapshot, error) {
		f.projections++
		if !reflect.DeepEqual(snapshot, f.snapshot) {
			t.Fatal("projection did not receive the committed snapshot")
		}
		return f.graph, f.projectionErr
	}
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(b), Bindings: b}).WithEngineController(e).Handler()
}

func snapshotRequest(h http.Handler, b *credentialBoundary, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if b.raw != "" {
		r.Header.Set("Authorization", "Bearer "+b.raw)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestEngineSnapshotHTTP(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "graph", true: "empty"}[empty], func(t *testing.T) {
			b := ownerBoundary()
			f := snapshotFixture(b)
			if empty {
				f.snapshot.Nodes, f.graph.Nodes, f.graph.Expansions = nil, nil, nil
			} else {
				f.snapshot.Nodes[0], f.snapshot.Nodes[1] = f.snapshot.Nodes[1], f.snapshot.Nodes[0]
			}
			w := snapshotRequest(snapshotServer(t, b, f), b, "GET", "/api/v1/runs/run/snapshot?team=forged&generation=999")
			if w.Code != 200 || f.reads != 1 || f.projections != 1 || f.writes != 0 {
				t.Fatalf("HTTP %d %s; reads=%d projections=%d writes=%d", w.Code, w.Body.String(), f.reads, f.projections, f.writes)
			}
			var got controllerwire.ScheduleSnapshot
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.RunID != "run" || !got.StoreNow.Equal(f.snapshot.StoreNow) || got.Jobs == nil || got.Generators == nil || len(got.Jobs) != len(f.graph.Nodes) || len(got.Generators) != len(f.graph.Expansions) {
				t.Fatalf("snapshot = %+v", got)
			}
			if !empty {
				if got.Jobs[0].ID != "first" || !got.Jobs[0].Ready || got.Jobs[1].ID != "generated" || !got.Jobs[1].Claimed || got.Jobs[1].Attempt != 2 || got.Jobs[1].StateSeq != 9 || !reflect.DeepEqual(got.Jobs[1].Declaration.Needs, []string{"first"}) {
					t.Fatalf("jobs = %+v", got.Jobs)
				}
				if got.Generators[0].ID != "empty" || got.Generators[0].Members == nil || len(got.Generators[0].Members) != 0 || !reflect.DeepEqual(got.Generators[1].Members, []string{"generated"}) {
					t.Fatalf("generators = %+v", got.Generators)
				}
			}
			if b.bindingRule != capability.BindRunOfTrigger || b.bindingTarget.Team != "team" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("unsafe snapshot: %v %s", w.Header(), w.Body.String())
			}
		})
	}
}

func TestEngineSnapshotHTTPRefusals(t *testing.T) {
	for _, state := range []string{"absent", "malformed", "revoked", "parent revoked", "membership lost", "lease expired", "stale generation", "wrong holder", "terminal owner", "cancelled", "wrong grant", "wrong run", "work", "plan", "member", "bootstrap", "nodehost", "binding", "missing run", "private read", "projection", "foreign team", "foreign run", "foreign job", "duplicate job", "missing job", "extra job", "bad declaration", "wrong declaration", "duplicate projection", "missing clock"} {
		t.Run(state, func(t *testing.T) {
			b := ownerBoundary()
			f := snapshotFixture(b)
			status, code, reads := 401, apierr.Unauthenticated, 0
			path := "/api/v1/runs/run/snapshot"
			switch state {
			case "absent":
				b.raw = ""
			case "malformed":
				b.raw = "bad"
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "parent revoked":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "membership lost":
				b.snapshot.Authorities[0].MembershipValid = false
			case "lease expired":
				b.snapshot.Binding.LeaseExpiresAt = b.snapshot.Now
			case "stale generation":
				b.snapshot.Binding.Current.Generation++
			case "wrong holder":
				b.snapshot.Binding.Current.Holder = "other"
			case "terminal owner":
				b.snapshot.Binding.FinishedAt = &b.snapshot.Now
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
			case "wrong grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
				status, code = 403, apierr.Forbidden
			case "wrong run":
				path = "/api/v1/runs/other/snapshot"
				status, code = 403, apierr.Forbidden
			case "work":
				b = newCredentialBoundary()
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TriggerHost}
				status, code = 403, apierr.Forbidden
			case "plan":
				b = planBoundary()
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TriggerHost}
				status, code = 403, apierr.Forbidden
			case "member":
				b = authorityMember()
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TriggerHost}
				status, code = 403, apierr.Forbidden
			case "bootstrap":
				b.snapshot.Authorities[0].Principal.Capability.Delivery = capability.DeliveryBootstrap
				b.snapshot.Authorities[0].Principal.Capability.Grants = capability.BootstrapGrants
				status, code = 403, apierr.Forbidden
			case "nodehost":
				b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
				b.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(b.raw))
				b.snapshot.Authorities[0].Principal.Capability.Audience = capability.NodeHost
				b.snapshot.Binding.ProcessAlive = true
				status, code = 403, apierr.Forbidden
			case "binding":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "wrong owner"}
				status, code = 403, apierr.Forbidden
			case "missing run":
				f.readErr = store.ErrNotFound
				status, code, reads = 404, apierr.NotFound, 1
			case "private read":
				f.readErr = errors.New("private-read")
				status, code, reads = 500, apierr.Internal, 1
			default:
				status, code, reads = 500, apierr.Internal, 1
				switch state {
				case "projection":
					f.projectionErr = errors.New("private-projection")
				case "foreign team":
					f.snapshot.Run.Team = "other"
				case "foreign run":
					f.snapshot.Run.ID = "other"
				case "foreign job":
					f.snapshot.Nodes[0].Ref.RunID = "other"
				case "duplicate job":
					f.snapshot.Nodes[1] = f.snapshot.Nodes[0]
				case "missing job":
					f.snapshot.Nodes = f.snapshot.Nodes[:1]
				case "extra job":
					f.graph.Nodes = f.graph.Nodes[:1]
				case "bad declaration":
					f.snapshot.Nodes[1].Spec = json.RawMessage(`broken`)
				case "wrong declaration":
					f.snapshot.Nodes[0].Spec = json.RawMessage(`{"id":"other"}`)
				case "duplicate projection":
					f.graph.Nodes[1] = f.graph.Nodes[0]
				case "missing clock":
					f.snapshot.StoreNow = time.Time{}
				}
			}
			w := snapshotRequest(snapshotServer(t, b, f), b, "GET", path)
			assertEngineError(t, w, status, code)
			if f.reads != reads || f.writes != 0 || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("reads=%d writes=%d body=%s", f.reads, f.writes, w.Body.String())
			}
		})
	}
}

func TestEngineSnapshotObservesCancellationInCommittedRead(t *testing.T) {
	b := ownerBoundary()
	f := snapshotFixture(b)
	f.snapshot.Run.CancelRequestedAt = &b.snapshot.Now
	f.graph.CancelRequested = true
	w := snapshotRequest(snapshotServer(t, b, f), b, "GET", "/api/v1/runs/run/snapshot")
	var got controllerwire.ScheduleSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !got.CancelRequested || f.reads != 1 {
		t.Fatalf("HTTP %d %s, reads=%d", w.Code, w.Body.String(), f.reads)
	}
}

func TestEngineSnapshotMissingDependencies(t *testing.T) {
	for _, state := range []string{"no policy", "no tenant", "nil tenant", "tenant error", "lookup error", "dead with lookup error"} {
		t.Run(state, func(t *testing.T) {
			b := ownerBoundary()
			e := &controller.EngineController{ResolveRunTeam: func(context.Context, string) (store.Team, error) { return "team", nil }}
			code := apierr.PolicyMissing
			switch state {
			case "nil tenant":
				e.ResolveTenant = func(context.Context, store.Team) (controller.RunTransactions, error) { return nil, nil }
			case "tenant error":
				e.ResolveTenant = func(context.Context, store.Team) (controller.RunTransactions, error) {
					return nil, errors.New("private-tenant")
				}
				code = apierr.Internal
			case "lookup error", "dead with lookup error":
				e.ResolveRunTeam = func(context.Context, string) (store.Team, error) { return "", errors.New("private-lookup") }
				code = apierr.Internal
				if state == "dead with lookup error" {
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
					code = apierr.Unauthenticated
				}
			}
			s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e)
			if state != "no policy" {
				s.WithAuthorization(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(b), Bindings: b})
			}
			w := snapshotRequest(s.Handler(), b, "GET", "/api/v1/runs/run/snapshot")
			assertEngineError(t, w, apierr.Status(code), code)
			if strings.Contains(w.Body.String(), "private-") {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestEngineSnapshotMethods(t *testing.T) {
	for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		w := snapshotRequest(controller.New(nil, nil).Handler(), ownerBoundary(), method, "/api/v1/runs/run/snapshot")
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		if w.Header().Get("Allow") != "GET" {
			t.Fatalf("Allow=%q", w.Header().Get("Allow"))
		}
	}
}
