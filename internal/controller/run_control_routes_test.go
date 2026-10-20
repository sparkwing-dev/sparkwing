package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type runControlWriterFake struct {
	RunTransactions
	team     store.Team
	seen     capability.Authorized
	decision controllerwire.ApprovalDecision
	job      string
	reason   string
	key      string
	calls    int
	failure  error
}

func (f *runControlWriterFake) Team() store.Team { return f.team }

func (f *runControlWriterFake) ReadRun(_ context.Context, id string) (store.RunRow, error) {
	return store.RunRow{ID: id, Team: f.team}, nil
}

func (f *runControlWriterFake) ResumeAuthorizedDebugPause(_ context.Context, a capability.Authorized) error {
	f.seen = a
	f.calls++
	return f.failure
}

func (f *runControlWriterFake) CreateAuthorizedDebugPause(_ context.Context, a capability.Authorized, job, reason string, _ time.Time, key string) error {
	f.seen, f.job, f.reason, f.key = a, job, reason, key
	f.calls++
	return f.failure
}

func (f *runControlWriterFake) ResolveAuthorizedApproval(_ context.Context, a capability.Authorized, decision controllerwire.ApprovalDecision) (controllerwire.ApprovalResult, error) {
	f.seen, f.decision = a, decision
	f.calls++
	now := time.Unix(100, 0).UTC()
	return controllerwire.ApprovalResult{Approval: controllerwire.Approval{
		RunID: a.Target.RunID, JobID: a.Target.NodeID, Resolution: decision.Resolution, Comment: decision.Comment,
		Approver: a.Principal.Capability.Subject.ID, RequestedAt: now, ResolvedAt: &now,
	}}, f.failure
}

func TestRunControlHTTPAuthorityProperty(t *testing.T) {
	property := func(input string, fail bool) bool {
		f := newScheduleHTTPFixture(t)
		f.facts.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl, capability.ApprovalsWrite}
		f.s.WithEngineController(&EngineController{ResolveTenant: selectorTenant(store.DefaultTeam, nil)})
		writer := &runControlWriterFake{team: store.DefaultTeam}
		if fail {
			writer.failure = &apierr.Error{Code: apierr.Forbidden, Message: "authority changed"}
		}
		testOnlyTenantPort[RunControlWriter](f.s, func(context.Context, store.Team) (RunControlWriter, error) { return writer, nil })
		key := "request-key"
		for _, pause := range []bool{false, true} {
			path := "/api/v1/runs/bound-run/approvals/bound-job"
			body := map[string]any{"resolution": "approved", "comment": input}
			want := 200
			if pause {
				path, body, want = "/api/v1/runs/bound-run/debug-pauses", map[string]any{"job_id": "bound-job", "reason": "pause:" + input}, 201
			}
			if fail {
				want = 403
			}
			data, err := json.Marshal(body)
			if err != nil {
				return false
			}
			r := httptest.NewRequest("POST", path+"?team=foreign&run_id=other", strings.NewReader(string(data)))
			r.Header.Set("Authorization", "Bearer "+f.raw)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", key)
			w := httptest.NewRecorder()
			f.s.Handler().ServeHTTP(w, r)
			if w.Code != want || writer.seen.Target.Team != string(store.DefaultTeam) || writer.seen.Target.RunID != "bound-run" || writer.seen.Principal.Capability.Subject.ID != "pusher" {
				t.Logf("HTTP %d: %s authority=%+v", w.Code, w.Body, writer.seen)
				return false
			}
			if pause && (writer.job != "bound-job" || writer.reason != "pause:"+input || writer.key != key) || !pause && (writer.seen.Target.NodeID != "bound-job" || writer.decision.IdempotencyKey != key) {
				return false
			}
			if !pause && !fail {
				var out controllerwire.ApprovalResult
				if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Approval.Approver != "pusher" || out.Approval.RunID != "bound-run" || out.Approval.JobID != "bound-job" {
					return false
				}
			}
		}
		return writer.calls == 2
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestRunControlHTTPRefusals(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl, capability.ApprovalsWrite}
	f.s.WithEngineController(&EngineController{ResolveTenant: selectorTenant(store.DefaultTeam, nil)})
	writer := &runControlWriterFake{team: store.DefaultTeam}
	testOnlyTenantPort[RunControlWriter](f.s, func(context.Context, store.Team) (RunControlWriter, error) { return writer, nil })
	for _, body := range []string{`{"resolution":"timed_out"}`, `{"resolution":"approved","approver":"forged"}`, `{"resolution":"approved","team":"foreign"}`, `{"resolution":"approved","idempotency_key":"body-key"}`} {
		f.call(t, "POST", "/api/v1/runs/bound-run/approvals/bound-job", body, "key", 400, nil)
	}
	f.call(t, "POST", "/api/v1/runs/bound-run/approvals/bound-job", `{"resolution":"approved"}`, "", 400, nil)
	for _, body := range []string{`{"job_id":"job","reason":"pause","released_by":"forged"}`, `{"node_id":"job","reason":"pause"}`, `{"job_id":"job"}`} {
		f.call(t, "POST", "/api/v1/runs/bound-run/debug-pauses", body, "key", 400, nil)
	}
	if writer.calls != 0 {
		t.Fatal("invalid input reached a transaction")
	}
	writer.team = "foreign"
	f.call(t, "POST", "/api/v1/runs/bound-run/approvals/bound-job", `{"resolution":"approved"}`, "key", 500, nil)
	if writer.calls != 0 {
		t.Fatal("foreign tenant reached a transaction")
	}
	testOnlyTenantPort[RunControlWriter](f.s, func(context.Context, store.Team) (RunControlWriter, error) { return nil, nil })
	f.call(t, "POST", "/api/v1/runs/bound-run/approvals/bound-job", `{"resolution":"approved"}`, "key", 500, nil)
}

func TestResumeDebugPauseWriterScopeAndErrors(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl}
	f.s.WithEngineController(&EngineController{ResolveTenant: selectorTenant(store.DefaultTeam, nil)})
	writer := &runControlWriterFake{team: store.DefaultTeam}
	testOnlyTenantPort[RunControlWriter](f.s, func(context.Context, store.Team) (RunControlWriter, error) { return writer, nil })
	f.call(t, "POST", "/api/v1/runs/run/jobs/job/resume", `{}`, "", 204, nil)
	if writer.calls != 1 || writer.seen.Target.RunID != "run" || writer.seen.Target.NodeID != "job" {
		t.Fatalf("resume=%+v calls=%d", writer.seen, writer.calls)
	}
	writer.failure = &apierr.Error{Code: apierr.Forbidden, Message: "authority changed"}
	f.call(t, "POST", "/api/v1/runs/run/jobs/job/resume", `{}`, "", 403, nil)
	writer.team = "foreign"
	f.call(t, "POST", "/api/v1/runs/run/jobs/job/resume", `{}`, "", 500, nil)
	if writer.calls != 2 {
		t.Fatal("foreign writer received resume")
	}
}

func TestBusinessRetiredRoutesAndExactMethods(t *testing.T) {
	s := New(nil, nil)
	h := s.Handler()
	for _, path := range []string{"/api/v1/runs/run/source-token", "/api/v1/runs/run/git-credential", "/api/v1/runs/run/nodes/job/start", "/api/v1/runs/run/nodes/job/finish", "/api/v1/concurrency/group/state", "/api/v1/runs/run/paused", "/api/v1/runs/run/nodes", "/api/v1/crons", "/api/v1/crons/repos", "/api/v1/crons/entry", "/api/v1/crons/entry/pause", "/api/v1/crons/entry/resume", "/api/v1/crons/entry/run", "/api/v1/crons/entry/disarm", "/api/v1/crons/entry/override", "/api/v1/runs/run/approvals/job/request"} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("Authorization", "Bearer swct_retired")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 404 || s.RegisteredRoute(r) {
				t.Fatalf("%s %s: HTTP %d %s", method, path, w.Code, w.Body)
			}
		}
	}
	for path, allow := range map[string]string{"/api/v1/schedules": "GET", "/api/v1/schedules/entry": "DELETE, GET", "/api/v1/runs/run/approvals/job": "GET, POST", "/api/v1/runs/run/debug-pauses": "GET, POST", "/api/v1/runners": "GET, POST"} {
		r := httptest.NewRequest("PATCH", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 405 || w.Header().Get("Allow") != allow || !strings.Contains(w.Body.String(), "method_not_allowed") {
			t.Fatalf("%s: HTTP %d %s Allow=%q", path, w.Code, w.Body, w.Header().Get("Allow"))
		}
	}
}

func TestMetricsHTTPDeploymentPolicy(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	p := &f.facts.snapshot.Authorities[0].Principal
	p.Capability.Team, p.Capability.Subject.Kind = "", capability.SubjectOperator
	p.Capability.Grants = []capability.Grant{capability.OperatorLimits}
	f.call(t, "GET", "/metrics", "", "", 200, nil)
	p.Capability.Subject.Kind, p.Capability.Team = capability.SubjectAccount, string(store.DefaultTeam)
	f.call(t, "GET", "/metrics", "", "", 403, nil)
	f.s.WithMetricsAddr("127.0.0.1:9000")
	f.call(t, "GET", "/metrics", "", "", 404, nil)
	if f.s.RegisteredRoute(httptest.NewRequest(http.MethodGet, "/metrics", nil)) {
		t.Fatal("dedicated metrics leaked into API")
	}
}

func TestBusinessWritesRequireVerifiedAuthority(t *testing.T) {
	s := New(newStoreForAuth(t), nil)
	for _, endpoint := range []struct{ method, path string }{{"POST", "/api/v1/team/invitations"}, {"DELETE", "/api/v1/team/invitations/invitation"}, {"PUT", "/api/v1/team/build-trust"}} {
		r := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer invalid")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("%s %s: HTTP %d %s", endpoint.method, endpoint.path, w.Code, w.Body)
		}
	}
}
