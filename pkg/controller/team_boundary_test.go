package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// tenancyFixture is a controller serving three teams: default, which holds
// the victim run an install had before teams, and two signed-up teams, A and
// B, each with its own owner session and bearer tokens. Team A also has a
// reader and an editor.
type tenancyFixture struct {
	t     *testing.T
	url   string
	st    *store.Store
	teamA *store.Tenant
	teamB *store.Tenant

	ownerA, ownerB   string
	readerA, editorA string
	// everyScopeB is a team B bearer carrying every scope a team's token may
	// hold, so a refusal it gets is the team boundary's and not a missing scope.
	everyScopeB string
	runnerB     string

	runA   string
	victim string
}

var everyScope = []string{
	controller.ScopeRunsRead, controller.ScopeRunsWrite, controller.ScopeRunsControl,
	controller.ScopeNodesClaim, controller.ScopeLogsRead, controller.ScopeLogsWrite,
	controller.ScopeTriggersRead, controller.ScopeTriggersClaim, controller.ScopeRunsState,
	controller.ScopeSecretsRead, controller.ScopeApprovalsWrite, controller.ScopeTeamAdmin,
}

func newTenancyFixture(t *testing.T, st *store.Store) *tenancyFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now); err != nil {
		t.Fatal(err)
	}
	srv := controller.New(st, nil).EnableAuthFromStore()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	f := &tenancyFixture{t: t, url: ts.URL, st: st}

	alice, teamA := signUp(t, st, "alice")
	bob, teamB := signUp(t, st, "bob")
	f.teamA = forTeam(t, st, teamA)
	f.teamB = forTeam(t, st, teamB)
	f.ownerA = session(t, st, alice, teamA)
	f.ownerB = session(t, st, bob, teamB)
	f.readerA = f.member(alice, "carol", store.RoleReader)
	f.editorA = f.member(alice, "dave", store.RoleEditor)

	raw, _, err := f.teamB.CreateToken(ctx, "b-everything", store.TokenKindUser, everyScope, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	f.everyScopeB = "Bearer " + raw
	raw, _, err = f.teamB.CreateToken(ctx, "b-runner", store.TokenKindRunner, []string{
		controller.ScopeRunsState, controller.ScopeNodesClaim, controller.ScopeTriggersClaim,
	}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	f.runnerB = "Bearer " + raw

	f.runA = "run-team-a"
	seedRun(t, f.teamA, f.runA, "build-a")
	if err := st.CreateNode(ctx, store.Node{RunID: f.runA, NodeID: "n1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateApproval(ctx, store.Approval{RunID: f.runA, NodeID: "n1", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.teamA.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "deploy", HolderID: "holder-a", RunID: f.runA, NodeID: "n1", Capacity: 1, Lease: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	f.victim = "victim-run"
	seedRun(t, forTeam(t, st, store.DefaultTeam), f.victim, "build-default")
	return f
}

func signUp(t *testing.T, st *store.Store, name string) (store.Account, store.Team) {
	t.Helper()
	res, err := st.ResolveSignIn(context.Background(), store.SignInProfile{
		Provider: "google", Subject: "sub-" + name, Email: name + "@example.test",
		EmailVerified: true, Name: name, GivenName: name,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if res.PersonalTeam == "" {
		t.Fatalf("%s signed up without a personal team", name)
	}
	return res.Account, res.PersonalTeam
}

func forTeam(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	tn, err := st.ForTeam(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

func session(t *testing.T, st *store.Store, acct store.Account, team store.Team) string {
	t.Helper()
	raw, _, _, err := st.CreateAccountSession(context.Background(), acct, team, time.Hour, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return "Session " + raw
}

// member signs name up and seats them in team A at role through an
// invitation from owner, which is how a member joins.
func (f *tenancyFixture) member(owner store.Account, name string, role store.Role) string {
	f.t.Helper()
	ctx := context.Background()
	acct, _ := signUp(f.t, f.st, name)
	inv, err := f.teamA.CreateInvitation(ctx, owner.ID, acct.Email, role, time.Now().UTC())
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.AcceptInvitation(ctx, acct.ID, inv.ID, time.Now().UTC()); err != nil {
		f.t.Fatal(err)
	}
	return session(f.t, f.st, acct, f.teamA.Team())
}

func seedRun(t *testing.T, tn *store.Tenant, id, pipeline string) {
	t.Helper()
	now := time.Now()
	if err := tn.CreateTriggerWithRun(context.Background(),
		store.Trigger{ID: id, Pipeline: pipeline, TriggerSource: "api", CreatedAt: now},
		store.Run{ID: id, Pipeline: pipeline, Status: "running", CreatedAt: now, StartedAt: now},
	); err != nil {
		t.Fatal(err)
	}
}

func (f *tenancyFixture) do(method, path, auth string, body any) (int, string) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, f.url+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func tenancyDialects(t *testing.T, run func(t *testing.T, f *tenancyFixture)) {
	for _, tc := range []struct {
		name string
		open func(*testing.T) *store.Store
	}{
		{name: "sqlite", open: openSQLiteBindingStore},
		{name: "postgres", open: openPostgresBindingStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, newTenancyFixture(t, tc.open(t)))
		})
	}
}

// The sequence an identity review ran against a build without the boundary: a
// fresh account in its own team listed, read and cancelled a run of another
// team and started a run that landed in default.
func TestTeamBoundary_AFreshAccountCannotReachAnotherTeamsRun(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := context.Background()
		for _, victim := range []string{f.victim, f.runA} {
			code, body := f.do("GET", "/api/v1/runs", f.ownerB, nil)
			if code != http.StatusOK || strings.Contains(body, victim) {
				t.Errorf("GET /runs as team B = %d listing %s: %s", code, victim, body)
			}
			if code, body := f.do("GET", "/api/v1/runs/"+victim, f.ownerB, nil); code != http.StatusNotFound {
				t.Errorf("GET /runs/%s as team B = %d want 404: %s", victim, code, body)
			}
			if code, body := f.do("POST", "/api/v1/runs/"+victim+"/cancel", f.ownerB, nil); code != http.StatusNotFound {
				t.Errorf("POST /runs/%s/cancel as team B = %d want 404: %s", victim, code, body)
			}
		}
		if n := cancelRequests(t, f.st); n != 0 {
			t.Errorf("team B's cancels reached %d triggers of other teams", n)
		}

		code, body := f.do("POST", "/api/v1/triggers", f.ownerB, map[string]any{
			"pipeline": "build-b", "trigger": map[string]any{"source": "api"},
		})
		if code != http.StatusAccepted {
			t.Fatalf("POST /triggers as team B = %d: %s", code, body)
		}
		var resp struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil || resp.RunID == "" {
			t.Fatalf("POST /triggers body %s: %v", body, err)
		}
		if _, err := f.teamB.GetRun(ctx, resp.RunID); err != nil {
			t.Errorf("the run team B started is not in team B: %v", err)
		}
		for _, other := range []*store.Tenant{f.teamA, forTeam(t, f.st, store.DefaultTeam)} {
			if _, err := other.GetRun(ctx, resp.RunID); err == nil {
				t.Errorf("the run team B started is readable in team %s", other.Team())
			}
		}
		if code, _ := f.do("GET", "/api/v1/runs/"+resp.RunID, f.ownerB, nil); code != http.StatusOK {
			t.Errorf("team B cannot read its own run: %d", code)
		}
		if code, _ := f.do("GET", "/api/v1/runs/"+resp.RunID, f.ownerA, nil); code != http.StatusNotFound {
			t.Errorf("team A reads team B's new run: %d", code)
		}
	})
}

func cancelRequests(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM triggers WHERE cancel_requested_at IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The orchestrator of a webhook trigger creates the run under its trigger
// claim, and the run lands in the team the claim was made in.
func TestTeamBoundary_ARunnerCreatesItsRunInItsOwnTeam(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := context.Background()
		if err := f.teamB.CreateTrigger(ctx, store.Trigger{
			ID: "run-by-b", Pipeline: "build-b", TriggerSource: "api", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		code, body := f.do("POST", "/api/v1/triggers/claim", f.runnerB, map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("claim as team B's runner = %d: %s", code, body)
		}
		var claimed store.Trigger
		if err := json.Unmarshal([]byte(body), &claimed); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", f.url+"/api/v1/runs", strings.NewReader(
			`{"id":"run-by-b","pipeline":"build-b","status":"running"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", f.runnerB)
		req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(claimed.ClaimSeq, 10))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST /runs under team B's trigger claim = %d: %s", resp.StatusCode, raw)
		}
		if _, err := f.teamB.GetRun(ctx, "run-by-b"); err != nil {
			t.Errorf("the run team B created is not in team B: %v", err)
		}
		if _, err := forTeam(t, f.st, store.DefaultTeam).GetRun(ctx, "run-by-b"); err == nil {
			t.Error("the run team B created landed in default")
		}
	})
}

// runRoutePath fills a run-scoped pattern with team A's run, so every route is
// asked about a row that exists and belongs to someone else.
func runRoutePath(pattern, runID string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	path = strings.NewReplacer("{id}", runID, "{nodeID}", "n1", "{path...}", "info/refs").Replace(path)
	return method, path
}

func runScoped(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	return strings.HasPrefix(path, "/api/v1/runs/{id}") || strings.HasPrefix(path, "/api/v1/triggers/{id}")
}

// Every route the controller registers under one run or trigger answers
// another team with 404, including the ones that change state. The list is
// read from server.go, so a route added later is checked without an edit
// here; the only way out is teamBoundaryExempt, which carries a reason.
func TestTeamBoundary_EveryRunRouteAnswersAnotherTeamWith404(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		routes := controller.MuxRouteScopes(t)
		mustCover := []string{
			"GET /api/v1/runs/{id}", "DELETE /api/v1/runs/{id}",
			"POST /api/v1/runs/{id}/cancel", "POST /api/v1/runs/{id}/retry",
			"POST /api/v1/runs/{id}/approvals/{nodeID}", "GET /api/v1/runs/{id}/events",
			"GET /api/v1/runs/{id}/nodes/{nodeID}/logs", "POST /api/v1/runs/{id}/nodes/{nodeID}/release",
			"POST /api/v1/runs/{id}/nodes/{nodeID}/bounce", "GET /api/v1/runs/{id}/receipt",
			"GET /api/v1/runs/{id}/attempts", "GET /api/v1/runs/{id}/steps",
			"POST /api/v1/triggers/{id}/claim", "GET /api/v1/triggers/{id}",
		}
		for _, p := range mustCover {
			if _, ok := routes[p]; !ok {
				t.Errorf("route %q is not in the list read from server.go; the enumeration is broken", p)
			}
		}
		for pattern, reason := range controller.TeamBoundaryExempt {
			if _, ok := routes[pattern]; !ok || strings.TrimSpace(reason) == "" {
				t.Errorf("exemption %q names no registered route or carries no reason", pattern)
			}
		}
		checked := 0
		for pattern := range routes {
			if !runScoped(pattern) {
				continue
			}
			if _, exempt := controller.TeamBoundaryExempt[pattern]; exempt {
				continue
			}
			checked++
			method, path := runRoutePath(pattern, f.runA)
			for _, caller := range []struct{ name, auth string }{
				{"team B owner session", f.ownerB},
				{"team B token with every scope", f.everyScopeB},
			} {
				code, body := f.do(method, path, caller.auth, map[string]any{})
				if code != http.StatusNotFound || strings.Contains(body, controller.UnsupportedRouteError) {
					t.Errorf("%s as %s = %d want the boundary's 404: %s", pattern, caller.name, code, body)
				}
			}
		}
		if checked < 60 {
			t.Errorf("checked %d run routes; server.go registers far more, so the enumeration is broken", checked)
		}

		ctx := context.Background()
		run, err := f.teamA.GetRun(ctx, f.runA)
		if err != nil {
			t.Fatalf("team B's requests removed team A's run: %v", err)
		}
		if run.Status != "running" {
			t.Errorf("team B's requests moved team A's run to %q", run.Status)
		}
		approval, err := f.st.GetApproval(ctx, f.runA, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if approval.ResolvedAt != nil {
			t.Errorf("team B resolved team A's approval: %+v", approval)
		}
		if n := cancelRequests(t, f.st); n != 0 {
			t.Errorf("team B's requests asked %d triggers to cancel", n)
		}
		if code, body := f.do("GET", "/api/v1/runs/"+f.runA, f.ownerA, nil); code != http.StatusOK {
			t.Errorf("team A cannot read its own run: %d %s", code, body)
		}
	})
}

// Lists answer only the caller's team. Each list is also read as team A, so a
// list that shows nothing to anyone cannot pass for a filtered one.
func TestTeamBoundary_ListsNeverIncludeAnotherTeam(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		lists := []struct {
			path    string
			ownerAs bool // team A's own read shows the run
		}{
			{"/api/v1/runs", true},
			{"/api/v1/triggers", true},
			{"/api/v1/approvals/pending", true},
			{"/api/v1/queue/state", true},
			{"/api/v1/pipelines/build-a/latest?status=running", true},
			{"/api/v1/agents", false},
			{"/api/v1/credits/history", false},
			{"/api/v1/triggers/spawned-child?parent_run_id=" + f.runA + "&parent_node_id=n1&pipeline=child", false},
		}
		for _, l := range lists {
			for _, caller := range []struct{ name, auth string }{
				{"team B owner session", f.ownerB},
				{"team B token with every scope", f.everyScopeB},
			} {
				code, body := f.do("GET", l.path, caller.auth, nil)
				if strings.Contains(body, f.runA) {
					t.Errorf("GET %s as %s = %d shows team A's run: %s", l.path, caller.name, code, body)
				}
			}
			if !l.ownerAs {
				continue
			}
			if code, body := f.do("GET", l.path, f.ownerA, nil); code != http.StatusOK || !strings.Contains(body, f.runA) {
				t.Errorf("GET %s as team A = %d does not show its own run: %s", l.path, code, body)
			}
		}
		if code, _ := f.do("GET", "/api/v1/concurrency/deploy/state", f.ownerB, nil); code != http.StatusNotFound {
			t.Errorf("team B reads team A's concurrency key: %d", code)
		}
		if code, body := f.do("GET", "/api/v1/concurrency/deploy/state", f.ownerA, nil); code != http.StatusOK || !strings.Contains(body, f.runA) {
			t.Errorf("team A cannot read its own concurrency key: %d %s", code, body)
		}
		if total := trendTotal(t, f, f.ownerB); total != 0 {
			t.Errorf("team B's trends count %d runs; it has none", total)
		}
		if total := trendTotal(t, f, f.ownerA); total != 1 {
			t.Errorf("team A's trends count %d runs, want its one", total)
		}
	})
}

func trendTotal(t *testing.T, f *tenancyFixture, auth string) int {
	t.Helper()
	code, body := f.do("GET", "/api/v1/trends", auth, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /trends = %d: %s", code, body)
	}
	var points []controller.TrendPoint
	if err := json.Unmarshal([]byte(body), &points); err != nil {
		var wrapped struct {
			Points []controller.TrendPoint `json:"points"`
		}
		if err := json.Unmarshal([]byte(body), &wrapped); err != nil {
			t.Fatalf("decode trends %s: %v", body, err)
		}
		points = wrapped.Points
	}
	total := 0
	for _, p := range points {
		total += p.Total
	}
	return total
}

func TestTeamBoundary_RolesStayInsideTheirGrant(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		if code, body := f.do("POST", "/api/v1/triggers", f.readerA, map[string]any{
			"pipeline": "build-a", "trigger": map[string]any{"source": "api"},
		}); code != http.StatusForbidden {
			t.Errorf("a reader triggered a run: %d %s", code, body)
		}
		if code, _ := f.do("GET", "/api/v1/runs/"+f.runA, f.readerA, nil); code != http.StatusOK {
			t.Errorf("a reader cannot read its team's run: %d", code)
		}
		if code, body := f.do("POST", "/api/v1/runs/"+f.runA+"/cancel", f.readerA, nil); code != http.StatusForbidden {
			t.Errorf("a reader cancelled a run: %d %s", code, body)
		}
		for _, r := range []struct{ method, path string }{
			{"POST", "/api/v1/secrets"},
			{"GET", "/api/v1/secrets"},
			{"GET", "/api/v1/secrets/deploy-key"},
			{"DELETE", "/api/v1/secrets/deploy-key"},
			{"POST", "/api/v1/secrets/rotate"},
			{"POST", "/api/v1/tokens"},
			{"GET", "/api/v1/tokens"},
			{"GET", "/api/v1/tokens/swu_x"},
			{"DELETE", "/api/v1/tokens/swu_x"},
			{"POST", "/api/v1/tokens/swu_x/rotate"},
		} {
			if code, body := f.do(r.method, r.path, f.editorA, map[string]any{}); code != http.StatusForbidden {
				t.Errorf("an editor reached %s %s: %d %s", r.method, r.path, code, body)
			}
		}
	})
}

// No membership reaches a route gated on admin, the deployment operator's
// scope. The admin routes are read from server.go and asked about team A's own
// run, so the refusal is the scope's and not the team boundary's.
// safety: these admin routes also admit team.admin because each acts only on
// the caller's own team: its webhook bindings, and a cache refresh.
var ownerAlsoAdmitted = map[string]bool{
	"POST /api/v1/webhooks/github/bindings":   true,
	"DELETE /api/v1/webhooks/github/bindings": true,
	"POST /api/v1/gitcache/refresh":           true,
}

func TestTeamBoundary_NoMemberReachesAnAdminRoute(t *testing.T) {
	for _, role := range []store.Role{store.RoleReader, store.RoleEditor, store.RoleOwner} {
		for _, s := range controller.ScopesForRole(role) {
			if s == controller.ScopeAdmin {
				t.Errorf("role %s carries the admin scope", role)
			}
		}
	}
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		checked := 0
		for pattern, scope := range controller.MuxRouteScopes(t) {
			// safety: the route list names the scope by its identifier in server.go.
			if scope != "ScopeAdmin" {
				continue
			}
			method, path := runRoutePath(pattern, f.runA)
			path = strings.NewReplacer("{name}", "x", "{prefix}", "swu_x", "{principal}", "p",
				"{key}", "k").Replace(path)
			for _, member := range []struct{ name, auth string }{
				{"owner", f.ownerA}, {"editor", f.editorA}, {"reader", f.readerA},
			} {
				if member.name == "owner" && ownerAlsoAdmitted[pattern] {
					continue
				}
				code, body := f.do(method, path, member.auth, map[string]any{})
				if code == http.StatusNotFound && strings.Contains(body, controller.UnsupportedRouteError) {
					continue
				}
				checked++
				if code != http.StatusForbidden {
					t.Errorf("%s reached admin route %s: %d %s", member.name, pattern, code, body)
				}
			}
		}
		if checked < 30 {
			t.Errorf("checked %d admin route calls; the enumeration is broken", checked)
		}
	})
}

func TestTeamBoundary_AClaimWhoseCredentialNamesNoTeamIsRefused(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		res, err := f.st.DB().ExecContext(context.Background(),
			`UPDATE tokens SET team = '' WHERE principal = 'b-runner'`)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("cleared the team of %d tokens want 1", n)
		}
		for path, body := range map[string]map[string]any{
			"/api/v1/triggers/claim": {},
			"/api/v1/nodes/claim":    {"holder_id": "runner-b"},
		} {
			code, body := f.do("POST", path, f.runnerB, body)
			if code != http.StatusForbidden || !strings.Contains(body, "claim_no_team") {
				t.Errorf("POST %s with a teamless credential = %d want 403 claim_no_team: %s", path, code, body)
			}
		}
	})
}

// A runner of a team other than default claims its team's trigger and carries
// the run to the end under that claim: the plan, the heartbeat and the finish
// are each fenced by the trigger claim, checked in the run's own team.
func TestTeamBoundary_ARunnerOfAnotherTeamFinishesTheRunItClaimed(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := context.Background()
		now := time.Now()
		const runB = "run-team-b"
		if err := f.teamB.CreateTriggerWithRun(ctx,
			store.Trigger{ID: runB, Pipeline: "build-b", TriggerSource: "api", CreatedAt: now},
			store.Run{ID: runB, Pipeline: "build-b", Status: "pending", CreatedAt: now, StartedAt: now},
		); err != nil {
			t.Fatal(err)
		}
		code, body := f.do("POST", "/api/v1/triggers/claim", f.runnerB, map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("claim as team B's runner = %d: %s", code, body)
		}
		var claimed store.Trigger
		if err := json.Unmarshal([]byte(body), &claimed); err != nil || claimed.ID != runB {
			t.Fatalf("claimed %s, want %s: %v", body, runB, err)
		}
		fenced := func(method, path string, payload any) (int, string) {
			t.Helper()
			b, _ := json.Marshal(payload)
			req, err := http.NewRequestWithContext(ctx, method, f.url+path, bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", f.runnerB)
			req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(claimed.ClaimSeq, 10))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(raw)
		}
		for _, step := range []struct {
			path    string
			payload any
		}{
			{"/api/v1/runs/" + runB + "/plan", map[string]any{"nodes": []any{}}},
			{"/api/v1/runs/" + runB + "/heartbeat", map[string]any{}},
			{"/api/v1/runs/" + runB + "/finish", map[string]any{"status": "success"}},
		} {
			if code, body := fenced("POST", step.path, step.payload); code/100 != 2 {
				t.Errorf("POST %s under team B's trigger claim = %d: %s", step.path, code, body)
			}
		}
		run, err := f.teamB.GetRun(ctx, runB)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "success" {
			t.Errorf("team B's run finished as %q, want success", run.Status)
		}
	})
}
