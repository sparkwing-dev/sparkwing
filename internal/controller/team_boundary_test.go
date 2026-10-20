package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func openPostgresStore(t *testing.T, opts ...store.Option) *store.Store {
	t.Helper()
	base := os.Getenv("SPARKWING_TEST_PG_URL")
	if strings.TrimSpace(base) == "" {
		if os.Getenv("SPARKWING_REQUIRE_PG") != "" {
			t.Fatal("SPARKWING_REQUIRE_PG is set, so SPARKWING_TEST_PG_URL must name a reachable Postgres")
		}
		t.Skip("SPARKWING_TEST_PG_URL not set; skipping the Postgres dialect")
	}
	schema := "cw_" + strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '_'
	}, t.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := teststore.OpenPostgres(ctx, base)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if _, err := teststore.Exec(ctx, admin, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	_ = admin.Close()
	t.Cleanup(func() {
		cleanup, err := teststore.OpenPostgres(context.Background(), base)
		if err != nil {
			t.Errorf("open postgres to drop schema %s: %v", schema, err)
			return
		}
		if _, err := teststore.Exec(context.Background(), cleanup, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		_ = cleanup.Close()
	})
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	st, err := teststore.OpenPostgres(ctx, base+sep+"search_path="+schema, opts...)
	if err != nil {
		t.Fatalf("open postgres schema %s: %v", schema, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openSQLiteStore(t *testing.T, opts ...store.Option) *store.Store {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), opts...)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type tenancyFixture struct {
	t     *testing.T
	srv   *controller.Server
	url   string
	st    *store.Store
	teamA *store.Tenant
	teamB *store.Tenant

	ownerA, ownerB   string
	readerA, editorA string
	everyScopeB      string
	runnerB          string

	runA     string
	victim   string
	requests int
}

var everyMemberGrant = store.ScopesForRole(store.RoleOwner)

func newTenancyFixture(t *testing.T, open func(*testing.T, ...store.Option) *store.Store) *tenancyFixture {
	t.Helper()
	ctx := context.Background()
	var srv *controller.Server
	st := open(t, store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		srv = controller.NewDeployment(st, deploymenttest.Team(t), nil).WithSecretsCipher(testCipher(t))
		return srv.StorePolicies(), nil
	}))
	now := time.Now().UTC()
	if _, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Team: "", Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "root"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}, ExpiresAt: teststore.CredentialExpiry(now, 0)}); err != nil {
		t.Fatal(err)
	}
	f := &tenancyFixture{t: t, srv: srv, url: "https://controller.test", st: st}

	alice, teamA := signUp(t, st, "alice")
	bob, teamB := signUp(t, st, "bob")
	f.teamA = forTeam(t, st, teamA)
	f.teamB = forTeam(t, st, teamB)
	f.ownerA = session(t, st, alice, teamA)
	f.ownerB = session(t, st, bob, teamB)
	f.readerA = f.member(alice, "carol", store.RoleReader)
	f.editorA = f.member(alice, "dave", store.RoleEditor)

	raw, _, err := teststore.ProvisionCredential(ctx, f.st, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(f.teamB.Team()), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "b-everything"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants(everyMemberGrant), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatal(err)
	}
	f.everyScopeB = "Bearer " + raw
	raw, _, err = teststore.ProvisionCredential(ctx, f.st, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Repositories: []string{"github.com/*/*", "git.example.test/*/*"}, Team: string(f.teamB.Team()), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "b-runner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, ExpiresAt: teststore.CredentialExpiry(now, 0), RegistrationID: "runner:" + string(f.teamB.Team()) + ":" + "b-runner"})
	if err != nil {
		t.Fatal(err)
	}
	f.runnerB = "Bearer " + raw

	f.runA = "run-team-a"
	seedRun(t, st, f.teamA, f.runA, "build-a")
	if err := teststore.SeedNode(st, ctx, store.Node{RunID: f.runA, NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, st).ExecContext(ctx, `INSERT INTO approvals(team,run_id,node_id,requested_at,message,on_timeout) VALUES ($1,$2,$3,$4,$5,$6)`, f.teamA.Team(), f.runA, "n1", now.UnixNano(), "", "fail"); err != nil {
		t.Fatal(err)
	}
	seedControllerCapacityHolder(t, st, f.teamA.Team(), "deploy", "holder-a", f.runA, "n1", 0)
	f.victim = "victim-run"
	seedRun(t, st, forTeam(t, st, store.DefaultTeam), f.victim, "build-default")
	return f
}

func signUp(t *testing.T, st *store.Store, name string) (store.Account, store.Team) {
	t.Helper()
	res, err := st.AsOperator().ResolveSignIn(context.Background(), store.SignInProfile{
		Provider: "google", Subject: "sub-" + name, Email: name + "@example.test",
		EmailVerified: true, Name: name, GivenName: name,
	}, store.SignUpConditions{}, time.Now().UTC())
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
	raw, _, _, err := st.AsOperator().CreateAccountSession(context.Background(), acct, team, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return "Session " + raw
}

func (f *tenancyFixture) member(owner store.Account, name string, role store.Role) string {
	f.t.Helper()
	ctx := context.Background()
	acct, _ := signUp(f.t, f.st, name)
	inv, err := fixtureInvitation(f.t, f.st, f.teamA, owner.ID, acct.Email, role)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.AsOperator().AcceptAccountInvitation(ctx, fixtureAccountAuthority(f.t, f.st, acct), inv.ID); err != nil {
		f.t.Fatal(err)
	}
	return session(f.t, f.st, acct, f.teamA.Team())
}

func seedRun(t *testing.T, st *store.Store, tn *store.Tenant, id, pipeline string) {
	t.Helper()
	now := time.Now()
	if err := teststore.SeedRun(st, t.Context(), tn.Team(), store.Run{ID: id, Pipeline: pipeline, Status: "running", CreatedAt: now, StartedAt: now}); err != nil {
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
	f.requests++
	req.Header.Set("Idempotency-Key", "tenant-"+strconv.Itoa(f.requests))
	if strings.HasPrefix(auth, "Session ") {
		if err := controller.ConsoleFixtureRequest(f.srv, req, strings.TrimPrefix(auth, "Session ")); err != nil {
			f.t.Fatal(err)
		}
	}
	req.Header.Set("Origin", "https://controller.test")
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func tenancyDialects(t *testing.T, run func(t *testing.T, f *tenancyFixture)) {
	for _, tc := range []struct {
		name string
		open func(*testing.T, ...store.Option) *store.Store
	}{
		{name: "sqlite", open: openSQLiteStore},
		{name: "postgres", open: openPostgresStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, newTenancyFixture(t, tc.open))
		})
	}
}

func TestTeamBoundary_ConcurrencyHolderCannotCrossTeams(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		path := "/api/v1/queue/groups/deploy"
		for _, route := range []string{"/heartbeat", "/release", "/holder?holder_id=holder-a"} {
			method := http.MethodPost
			payload := map[string]any{"holder_id": "holder-a"}
			if route == "/release" {
				payload["outcome"] = "success"
			}
			if strings.HasPrefix(route, "/holder") {
				method, payload = http.MethodGet, nil
			}
			code, body := f.do(method, path+route, f.everyScopeB, payload)
			if code != http.StatusForbidden || strings.Contains(body, "route not found") {
				t.Errorf("team B reached team A's concurrency holder through %s: %d", route, code)
			}
		}
		state, err := f.teamA.GetConcurrencyState(t.Context(), "deploy")
		if err != nil || len(state.Holders) != 1 || state.Holders[0].HolderID != "holder-a" || state.Holders[0].RunID != f.runA || !state.Holders[0].LeaseExpiresAt.After(time.Now()) {
			t.Fatalf("team A holder changed: %+v, %v", state, err)
		}
	})
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

		code, body := f.do("POST", "/api/v1/runs", f.ownerB, computeIntake("build-b"))
		if code != http.StatusCreated {
			t.Fatalf("POST /runs as team B = %d: %s", code, body)
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
	if err := teststore.SQL(t, st).QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs WHERE cancel_requested_at IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func runRoutePath(pattern, runID string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	path = strings.NewReplacer("{run}", runID, "{job}", "n1", "{path...}", "info/refs").Replace(path)
	return method, path
}

func runScoped(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	return strings.HasPrefix(path, "/api/v1/runs/{run}")
}

func TestTeamBoundary_EveryRunRouteHidesOtherTeamsResources(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		routes := controller.New(nil, nil).Registrations()
		mustCover := []string{
			"GET /api/v1/runs/{run}", "DELETE /api/v1/runs/{run}",
			"POST /api/v1/runs/{run}/cancel", "POST /api/v1/runs/{run}/retry",
			"POST /api/v1/runs/{run}/approvals/{job}", "GET /api/v1/runs/{run}/events",
			"GET /api/v1/runs/{run}/jobs/{job}/logs", "POST /api/v1/runs/{run}/debug-pauses",
			"GET /api/v1/runs/{run}/receipt",
			"GET /api/v1/runs/{run}/attempts", "GET /api/v1/runs/{run}/steps",
			"POST /api/v1/runs/{run}/claim",
		}
		for _, p := range mustCover {
			if !slices.Contains(routes, p) {
				t.Errorf("route %q is missing from serving registrations", p)
			}
		}
		checked := 0
		for _, pattern := range routes {
			if !runScoped(pattern) {
				continue
			}
			checked++
			method, path := runRoutePath(pattern, f.runA)
			for _, caller := range []struct{ name, auth string }{
				{"team B owner session", f.ownerB},
				{"team B token with every member grant", f.everyScopeB},
			} {
				code, body := f.do(method, path, caller.auth, map[string]any{})
				missingMethod, missingPath := runRoutePath(pattern, "missing-run")
				missingCode, missingBody := f.do(missingMethod, missingPath, caller.auth, map[string]any{})
				if code != missingCode || body != missingBody || (code != http.StatusForbidden && code != http.StatusNotFound) || strings.Contains(body, `"message":"route not found"`) {
					t.Errorf("%s as %s reveals resource existence: foreign=%d %s missing=%d %s", pattern, caller.name, code, body, missingCode, missingBody)
				}
				if slices.Contains(mustCover, pattern) && !strings.HasSuffix(pattern, "/claim") && code != http.StatusNotFound {
					t.Errorf("member route %s returned %d, want 404: %s", pattern, code, body)
				}
			}
		}
		if checked < len(mustCover) {
			t.Errorf("checked %d run routes, want at least %d", checked, len(mustCover))
		}
		for _, named := range []map[string]any{
			{"runner_version": "test", "kinds": []string{"node"}, "labels": []string{}, "job_id": "n1"},
			{"runner_version": "test", "kinds": []string{"trigger"}, "labels": []string{}},
			{"runner_version": "test", "kinds": []string{"node", "trigger"}, "labels": []string{}},
		} {
			foreign, missing := maps.Clone(named), maps.Clone(named)
			foreign["run_id"], missing["run_id"] = f.runA, "missing-run"
			code, body := f.do("POST", "/api/v1/runners/claim", f.runnerB, foreign)
			missingCode, missingBody := f.do("POST", "/api/v1/runners/claim", f.runnerB, missing)
			if code != missingCode || body != missingBody || code == http.StatusOK {
				t.Errorf("runner claim naming %v reveals run existence: foreign=%d %s missing=%d %s", named["kinds"], code, body, missingCode, missingBody)
			}
		}

		ctx := context.Background()
		run, err := f.teamA.GetRun(ctx, f.runA)
		if err != nil {
			t.Fatalf("team B's requests removed team A's run: %v", err)
		}
		if run.Status != "running" {
			t.Errorf("team B's requests moved team A's run to %q", run.Status)
		}
		approval, err := f.teamA.GetApproval(ctx, f.runA, "n1")
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
		readers := map[store.Team]*queueGroupReaderFake{}
		controller.BindTenantPortForTest[controller.ConcurrencyStore](f.srv, func(ctx context.Context, team store.Team) (controller.ConcurrencyStore, error) {
			tenant, err := f.st.ForTeam(ctx, team)
			if err != nil {
				return nil, err
			}
			reader := &queueGroupReaderFake{tenant: tenant}
			readers[team] = reader
			return reader, nil
		})
		lists := []struct {
			path    string
			ownerAs bool
		}{
			{"/api/v1/runs", true},
			{"/api/v1/approvals", true},
			{"/api/v1/pipelines/build-a/latest?status=running", true},
			{"/api/v1/runners", false},
			{"/api/v1/credits/history", false},
		}
		for _, l := range lists {
			for _, caller := range []struct{ name, auth string }{
				{"team B owner session", f.ownerB},
				{"team B token with every member grant", f.everyScopeB},
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
		if code, _ := f.do("GET", "/api/v1/queue/groups/deploy", f.ownerB, nil); code != http.StatusNotFound {
			t.Errorf("team B reads team A's concurrency key: %d", code)
		}
		if code, body := f.do("GET", "/api/v1/queue/groups/deploy", f.ownerA, nil); code != http.StatusOK || !strings.Contains(body, f.runA) {
			t.Errorf("team A cannot read its own concurrency key: %d %s", code, body)
		}
		for _, team := range []store.Team{f.teamA.Team(), f.teamB.Team()} {
			reader := readers[team]
			if reader == nil || reader.calls != 1 || reader.seen.Target.Team != string(team) || reader.seen.Target.SlotKey != "deploy" || reader.seen.Principal.Capability.Team != string(team) {
				t.Fatalf("queue read did not preserve authority: %+v", reader)
			}
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
		if code, body := f.do("POST", "/api/v1/runs", f.readerA, computeIntake("build-a")); code != http.StatusForbidden {
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
			{"GET", "/api/v1/secrets/deploy-key"},
			{"DELETE", "/api/v1/secrets/deploy-key"},
			{"POST", "/api/v1/secrets/rotate"},
		} {
			if code, body := f.do(r.method, r.path, f.editorA, map[string]any{}); code != http.StatusForbidden {
				t.Errorf("an editor reached %s %s: %d %s", r.method, r.path, code, body)
			}
		}
		for _, grant := range []string{"team.admin", "secrets.read", "nodes.claim", "triggers.claim", "operator.tokens"} {
			if code, body := f.do("POST", "/api/v1/tokens", f.editorA, map[string]any{"grants": []string{grant}}); code != http.StatusForbidden {
				t.Fatalf("editor issued %s = %d: %s", grant, code, body)
			}
		}
		code, body := f.do("POST", "/api/v1/tokens", f.editorA, map[string]any{"grants": []string{"runs.read"}})
		var issued struct{ Capability, Prefix, Team string }
		if code != http.StatusCreated || json.Unmarshal([]byte(body), &issued) != nil || issued.Capability == "" || issued.Prefix == "" || issued.Team != string(f.teamA.Team()) {
			t.Fatalf("editor read token = %d: %s", code, body)
		}
		if code, body := f.do("GET", "/api/v1/tokens", f.editorA, nil); code != 200 || !strings.Contains(body, issued.Prefix) || strings.Contains(body, issued.Capability) {
			t.Fatalf("editor own inventory = %d: %s", code, body)
		}
		for _, auth := range []string{f.readerA, f.ownerB} {
			for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"DELETE", ""}, {"POST", "/rotate"}} {
				if code, body := f.do(route.method, "/api/v1/tokens/"+issued.Prefix+route.suffix, auth, nil); code != http.StatusForbidden || strings.Contains(body, issued.Capability) {
					t.Fatalf("foreign token operation = %d: %s", code, body)
				}
			}
		}
		if code, body := f.do("POST", "/api/v1/runs/"+f.runA+"/cancel", "Bearer "+issued.Capability, nil); code != http.StatusForbidden {
			t.Fatalf("read token cancellation = %d: %s", code, body)
		}
	})
}

// safety: these admin routes also admit team.admin because each acts only on
// the caller's own team's secrets.
var ownerAlsoAdmitted = map[string]bool{
	"POST /api/v1/secrets":          true,
	"GET /api/v1/secrets":           true,
	"DELETE /api/v1/secrets/{name}": true,
}

func TestTeamBoundary_NoMemberReachesAnAdminRoute(t *testing.T) {
	for _, role := range []store.Role{store.RoleReader, store.RoleEditor, store.RoleOwner} {
		for _, s := range store.ScopesForRole(role) {
			switch s {
			case "admin", "secrets.read", "nodes.claim",
				"triggers.claim", "runs.state":
				t.Errorf("role %s carries the %s scope", role, s)
			}
		}
	}
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		checked := 0
		for _, pattern := range controller.New(nil, nil).Registrations() {
			method, path, _ := strings.Cut(pattern, " ")
			policy, ok := capability.LookupRoute(method, path)
			if !ok {
				t.Fatalf("registered route %s has no policy", pattern)
			}
			if policy.Owner != capability.OwnerDeployment {
				continue
			}
			method, path = runRoutePath(pattern, f.runA)
			path = strings.NewReplacer("{name}", "x", "{prefix}", "swu_x", "{principal}", "p",
				"{key}", "k").Replace(path)
			for _, member := range []struct{ name, auth string }{
				{"owner", f.ownerA}, {"editor", f.editorA}, {"reader", f.readerA},
			} {
				if member.name == "owner" && ownerAlsoAdmitted[pattern] {
					continue
				}
				code, body := f.do(method, path, member.auth, map[string]any{})
				if code == http.StatusNotFound && strings.Contains(body, `"message":"route not found"`) {
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

// An owner manages its own team's secrets and none of another team's; a
// name another team holds reads as missing. Members list their own team's
// names and variables, and a session never reads a masked value back.
func TestTeamBoundary_SecretsStayWithTheirTeam(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		for _, row := range []map[string]any{
			{"name": "DEPLOY_KEY", "value": "team-a-secret-value"},
			{"name": "REGION", "value": "team-a-region", "masked": false, "shared": true},
		} {
			if code, body := f.do("POST", "/api/v1/secrets", f.ownerA, row); code != http.StatusNoContent {
				t.Fatalf("team A owner set %v = %d: %s", row["name"], code, body)
			}
		}
		for _, member := range []struct{ name, auth string }{{"owner", f.ownerA}, {"editor", f.editorA}, {"reader", f.readerA}} {
			code, body := f.do("GET", "/api/v1/secrets", member.auth, nil)
			if code != http.StatusOK || !strings.Contains(body, "DEPLOY_KEY") || !strings.Contains(body, "team-a-region") {
				t.Errorf("team A %s's list = %d without its rows: %s", member.name, code, body)
			}
			if strings.Contains(body, "team-a-secret-value") {
				t.Errorf("team A %s's list carries the secret's value: %s", member.name, body)
			}
		}
		if code, body := f.do("GET", "/api/v1/secrets/DEPLOY_KEY", f.ownerA, nil); code != http.StatusForbidden || strings.Contains(body, "team-a-secret-value") {
			t.Errorf("team A owner session read its secret back = %d: %s", code, body)
		}
		for _, other := range []struct{ name, auth string }{{"team B owner", f.ownerB}, {"team B token", f.everyScopeB}} {
			if code, body := f.do("GET", "/api/v1/secrets", other.auth, nil); code != http.StatusOK || strings.Contains(body, "DEPLOY_KEY") || strings.Contains(body, "REGION") {
				t.Errorf("GET /secrets as %s = %d shows team A's rows: %s", other.name, code, body)
			}
			for _, name := range []string{"DEPLOY_KEY", "REGION"} {
				if code, body := f.do("GET", "/api/v1/secrets/"+name, other.auth, nil); code != http.StatusNotFound || strings.Contains(body, "team-a-") {
					t.Errorf("GET /secrets/%s as %s = %d want 404: %s", name, other.name, code, body)
				}
			}
			if code, body := f.do("DELETE", "/api/v1/secrets/DEPLOY_KEY", other.auth, nil); code != http.StatusNotFound {
				t.Errorf("DELETE /secrets/DEPLOY_KEY as %s = %d want 404: %s", other.name, code, body)
			}
		}
		if code, body := f.do("GET", "/api/v1/secrets", f.runnerB, nil); code != http.StatusForbidden {
			t.Errorf("a runner token listed secrets: %d %s", code, body)
		}
		if code, body := f.do("GET", "/api/v1/secrets", f.ownerA, nil); code != http.StatusOK || !strings.Contains(body, "DEPLOY_KEY") {
			t.Errorf("team B's delete removed team A's secret: %d %s", code, body)
		}
	})
}

func fixtureInvitation(t *testing.T, st *store.Store, tenant *store.Tenant, actor, email string, role store.Role) (store.Invitation, error) {
	t.Helper()
	account, err := st.AsOperator().Account(t.Context(), actor)
	if err != nil {
		return store.Invitation{}, err
	}
	raw := strings.TrimPrefix(session(t, st, account, tenant.Team()), "Session ")
	route, _ := capability.LookupRoute("POST", "/api/v1/team/invitations")
	target := capability.Target{Team: string(tenant.Team())}
	auth, err := capability.NewAuthenticator(st.AsOperator()).Authenticate(t.Context(), capability.Presentation{Raw: raw, Class: capability.CredentialSession, Evidence: capability.RequestEvidence{Target: target}}, route)
	if err != nil {
		return store.Invitation{}, err
	}
	a, err := capability.Authorize(t.Context(), st.AsOperator(), route, auth, target)
	if err != nil {
		return store.Invitation{}, err
	}
	return tenant.CreateAuthorizedInvitation(t.Context(), a, email, role, email)
}

func fixtureAccountAuthority(t *testing.T, st *store.Store, account store.Account) capability.Authorized {
	t.Helper()
	raw := strings.TrimPrefix(session(t, st, account, account.ActiveTeam), "Session ")
	route, _ := capability.LookupRoute("POST", "/api/v1/me/active-team")
	auth, err := capability.NewAuthenticator(st.AsOperator()).Authenticate(t.Context(), capability.Presentation{Raw: raw, Class: capability.CredentialSession}, route)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(t.Context(), st.AsOperator(), route, auth, capability.Target{AccountID: account.ID})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
