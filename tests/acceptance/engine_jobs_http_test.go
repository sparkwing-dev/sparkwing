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

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type jobViewTransactions struct {
	snapshotTransactions
	nodeReads int
	nodeRun   string
}

func (f *jobViewTransactions) ReadStatus(_ context.Context, team store.Team, run string) (controller.RunStatusFacts, error) {
	f.nodeReads++
	f.nodeRun = run
	if team != "team" {
		return controller.RunStatusFacts{}, errors.New("unexpected tenant")
	}
	snapshot := f.snapshot
	snapshot.Nodes = []runstate.NodeRow{f.node}
	return controller.RunStatusFacts{Snapshot: snapshot}, f.readErr
}

func jobViewFixture(b *credentialBoundary) *jobViewTransactions {
	f := &jobViewTransactions{snapshotTransactions: *snapshotFixture(b)}
	f.snapshot.Run.Number = 1
	f.node = runstate.NodeRow{
		Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Status: runstate.StatusRunning,
		Generation: 4, Attempt: 2, StateSeq: 9, Expansion: runstate.ExpansionExpected,
		Spec: json.RawMessage(`{"id":"job","env":{"TOKEN":"private-secret"}}`), SpecHash: "private-spec-hash",
		CapabilityPrefix: "private-prefix", ClaimedBy: "private-holder", StartedAt: &b.snapshot.Now,
	}
	f.snapshot.Nodes = []runstate.NodeRow{f.node}
	return f
}

func jobViewServer(t *testing.T, b *credentialBoundary, f *jobViewTransactions) http.Handler {
	t.Helper()
	e := fakeEngine(t, &f.engineTransactions)
	e.ReadRunStatus = f.ReadStatus
	e.ResolveTenant = func(_ context.Context, team store.Team) (controller.TenantHandle, error) {
		if team != "team" {
			return nil, store.ErrNotFound
		}
		return f, nil
	}
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithAuthorization(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(b), Bindings: b}).
		WithEngineController(e).Handler()
}

func jobViewBoundary(identity string) *credentialBoundary {
	switch identity {
	case "member":
		b := intakeBoundary()
		b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
		return b
	case "owner":
		return ownerBoundary()
	default:
		b := newCredentialBoundary()
		b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.DataRead}
		return b
	}
}

func TestEngineJobsHTTP(t *testing.T) {
	for _, identity := range []string{"member", "owner", "job"} {
		for _, list := range []bool{false, true} {
			t.Run(identity+map[bool]string{false: "/detail", true: "/list"}[list], func(t *testing.T) {
				b := jobViewBoundary(identity)
				f := jobViewFixture(b)
				path := "/api/v1/runs/run/jobs"
				if !list {
					path += "/job"
				}
				w := snapshotRequest(jobViewServer(t, b, f), b, "GET", path+"?team=other&generation=99")
				if identity == "job" && list {
					assertEngineError(t, w, 403, apierr.Forbidden)
					if f.reads != 0 || f.nodeReads != 0 {
						t.Fatal("job authority listed its siblings")
					}
					return
				}
				want := controllerwire.JobDetail{ID: "job", Status: "running", Generation: 4, Attempt: 2, StateSeq: 9, Expansion: "expected", StartedAt: &b.snapshot.Now}
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-") {
					t.Fatalf("HTTP %d %s headers=%v", w.Code, w.Body.String(), w.Header())
				}
				if list {
					var got []controllerwire.JobDetail
					if json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, []controllerwire.JobDetail{want}) || f.reads != 1 || f.nodeReads != 0 {
						t.Fatalf("list=%s reads=%d nodeReads=%d", w.Body.String(), f.reads, f.nodeReads)
					}
				} else {
					var got controllerwire.DebugJob
					if json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got.Job, want) || got.RunID != "run" || got.Number != f.snapshot.Run.Number || got.Execution != nil || f.reads != 0 || f.nodeReads != 1 || runspec.RunID(f.nodeRun) != f.node.Ref.RunID {
						t.Fatalf("detail=%s reads=%d nodeReads=%d ref=%+v", w.Body.String(), f.reads, f.nodeReads, f.nodeRun)
					}
				}
				if b.bindingTarget.Team != "team" || b.bindingTarget.RunID != "run" || f.projections != 0 || f.writes != 0 {
					t.Fatalf("target=%+v projections=%d writes=%d", b.bindingTarget, f.projections, f.writes)
				}
			})
		}
	}
}

func TestEngineJobsHTTPRefusals(t *testing.T) {
	for _, list := range []bool{false, true} {
		for _, state := range []string{"absent", "revoked", "parent revoked", "membership lost", "expired lease", "stale generation", "wrong holder", "ended claim", "cancelled", "missing grant", "nodehost", "bootstrap", "wrong run", "wrong job", "foreign team", "relational refusal", "missing", "private error", "foreign persisted run", "empty persisted job", "duplicate persisted job"} {
			if list && state == "wrong job" || !list && state == "duplicate persisted job" {
				continue
			}
			t.Run(map[bool]string{false: "detail/", true: "list/"}[list]+state, func(t *testing.T) {
				b := ownerBoundary()
				if !list {
					b = jobViewBoundary("job")
				}
				f := jobViewFixture(b)
				path := "/api/v1/runs/run/jobs"
				if !list {
					path += "/job"
				}
				status, code, reads := 401, apierr.Unauthenticated, 0
				switch state {
				case "absent":
					b.raw = ""
				case "revoked":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
				case "parent revoked":
					b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
				case "membership lost":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
				case "expired lease":
					b.snapshot.Binding.LeaseExpiresAt = b.snapshot.Now
				case "stale generation":
					b.snapshot.Binding.Current.Generation++
				case "wrong holder":
					b.snapshot.Binding.Current.Holder = "other"
				case "ended claim":
					b.snapshot.Binding.FinishedAt = &b.snapshot.Now
				case "cancelled":
					b.snapshot.Binding.RunCancelled = true
					status, code = 403, apierr.Forbidden
				case "missing grant":
					b.snapshot.Authorities[0].Principal.Capability.Grants = nil
					status, code = 403, apierr.Forbidden
				case "nodehost":
					b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
					p := &b.snapshot.Authorities[0].Principal
					p.Capability.Digest = sha256.Sum256([]byte(b.raw))
					p.Capability.Audience = capability.NodeHost
					b.snapshot.Binding.ProcessAlive = true
					status, code = 403, apierr.Forbidden
				case "bootstrap":
					b.snapshot.Authorities[0].Principal.Capability.Delivery = capability.DeliveryBootstrap
					b.snapshot.Authorities[0].Principal.Capability.Grants = capability.BootstrapGrants()
					status, code = 403, apierr.Forbidden
				case "wrong run":
					path = strings.Replace(path, "/run/", "/other/", 1)
					status, code = 403, apierr.Forbidden
				case "wrong job":
					path = strings.TrimSuffix(path, "/job") + "/other"
					status, code = 403, apierr.Forbidden
				case "foreign team":
					b.snapshot.Authorities[0].Principal.Capability.Team = "other"
					status, code = 404, apierr.NotFound
				case "relational refusal":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "wrong resource"}
					status, code = 403, apierr.Forbidden
				case "missing":
					f.readErr = store.ErrNotFound
					status, code, reads = 404, apierr.NotFound, 1
				case "private error":
					f.readErr = errors.New("private credential")
					status, code, reads = 500, apierr.Internal, 1
				default:
					status, code, reads = 500, apierr.Internal, 1
					switch state {
					case "foreign persisted run":
						f.node.Ref.RunID = "other"
						f.snapshot.Nodes[0].Ref.RunID = "other"
					case "empty persisted job":
						f.node.Ref.NodeID = ""
						f.snapshot.Nodes[0].Ref.NodeID = ""
					case "duplicate persisted job":
						f.snapshot.Nodes = append(f.snapshot.Nodes, f.node)
					}
				}
				w := snapshotRequest(jobViewServer(t, b, f), b, "GET", path)
				assertEngineError(t, w, status, code)
				if f.reads+f.nodeReads != reads || f.writes != 0 || strings.Contains(w.Body.String(), "private-") {
					t.Fatalf("reads=%d nodeReads=%d writes=%d body=%s", f.reads, f.nodeReads, f.writes, w.Body.String())
				}
			})
		}
	}
}

func TestEngineJobsEmptyListAndMethods(t *testing.T) {
	b := jobViewBoundary("member")
	f := jobViewFixture(b)
	f.snapshot.Nodes = nil
	w := snapshotRequest(jobViewServer(t, b, f), b, "GET", "/api/v1/runs/run/jobs")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/runs/run/jobs", "/api/v1/runs/run/jobs/job"} {
		w = snapshotRequest(jobViewServer(t, b, f), b, "HEAD", path)
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
	}
}

func TestEngineJobsMemberReadsFinishedJobs(t *testing.T) {
	b := jobViewBoundary("member")
	f := jobViewFixture(b)
	b.snapshot.Binding.RunCancelled, b.snapshot.Binding.RunFinished = true, true
	f.node.Status, f.node.Outcome, f.node.StatusDetail = runstate.StatusDone, runstate.NodeCancelled, "user requested"
	f.node.FinishedAt, f.node.CancelRequestedAt = &b.snapshot.Now, &b.snapshot.Now
	f.snapshot.Nodes = []runstate.NodeRow{f.node}
	for _, path := range []string{"/api/v1/runs/run/jobs", "/api/v1/runs/run/jobs/job"} {
		w := snapshotRequest(jobViewServer(t, b, f), b, "GET", path)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"outcome":"cancelled"`) || !strings.Contains(w.Body.String(), `"status_detail":"user requested"`) || !strings.Contains(w.Body.String(), `"finished_at":`) || !strings.Contains(w.Body.String(), `"cancel_requested_at":`) {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
	}
}

func TestEngineJobsRemovedNodeReads(t *testing.T) {
	b := jobViewBoundary("member")
	f := jobViewFixture(b)
	s := jobViewServer(t, b, f)
	for _, path := range []string{"/api/v1/runs/run/nodes", "/api/v1/runs/run/nodes/job"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+b.raw)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 404 && w.Code != 405 || f.reads != 0 || f.nodeReads != 0 {
			t.Fatalf("HTTP %d %s reads=%d nodeReads=%d", w.Code, w.Body.String(), f.reads, f.nodeReads)
		}
	}
}
