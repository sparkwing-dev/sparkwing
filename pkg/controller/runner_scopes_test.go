package controller_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var runnerScopes = []string{
	controller.ScopeNodesClaim,
	controller.ScopeTriggersClaim,
	controller.ScopeRunsState,
	controller.ScopeSecretsRead,
	controller.ScopeLogsWrite,
}

type scopedFixture struct {
	url   string
	store *store.Store
}

func newScopedFixture(t *testing.T, scopes []string) (scopedFixture, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if _, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, now); err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	raw, _, err := st.CreateToken("pool", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatalf("CreateToken runner: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return scopedFixture{url: srv.URL, store: st}, raw
}

func seedRepoTrigger(t *testing.T, st *store.Store, id, repo string) {
	t.Helper()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: id, Pipeline: "deploy", Repo: repo, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTrigger %s: %v", id, err)
	}
}

func seedSecret(t *testing.T, st *store.Store, name, value, repo string, shared bool) {
	t.Helper()
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: name, Value: value, Principal: "root", Repo: repo, Masked: true, Shared: shared,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret %s/%s: %v", name, repo, err)
	}
}

// The whole controller conversation a trigger-handling runner has, in
// order. Every step runs on the documented runner scope set, so a route
// that regains an `admin` requirement fails this test rather than the
// operator's first pipeline.
func TestRunnerScopes_DocumentedSetCompletesARunWithoutAdmin(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)

	trig, err := c.ClaimTrigger(ctx)
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if trig == nil {
		t.Fatal("ClaimTrigger returned no trigger; the queue was seeded with one")
	}
	gotTrigger, err := c.GetTrigger(ctx, trig.ID)
	if err != nil {
		t.Fatalf("GetTrigger while holding the trigger claim: %v", err)
	}
	if gotTrigger.ID != trig.ID {
		t.Fatalf("GetTrigger ID = %q, want %q", gotTrigger.ID, trig.ID)
	}
	if _, err := c.HeartbeatTrigger(ctx, trig.ID); err != nil {
		t.Fatalf("HeartbeatTrigger: %v", err)
	}

	for _, step := range []struct {
		name string
		call func() error
	}{
		{"create the run", func() error {
			return c.CreateRun(ctx, store.Run{
				ID: trig.ID, Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
			})
		}},
		{"persist the plan snapshot", func() error {
			return c.UpdatePlanSnapshot(ctx, trig.ID, []byte(`{"nodes":[]}`))
		}},
		{"create the build node", func() error {
			return c.CreateNode(ctx, store.Node{RunID: trig.ID, NodeID: "build", Status: "pending"})
		}},
		{"create the deploy node", func() error {
			return c.CreateNode(ctx, store.Node{RunID: trig.ID, NodeID: "deploy", Status: "pending"})
		}},
		{"expand dependencies", func() error {
			return c.UpdateNodeDeps(ctx, trig.ID, "deploy", []string{"build"})
		}},
		{"append a run event", func() error {
			return c.AppendEvent(ctx, trig.ID, "build", "node_queued", []byte(`{}`))
		}},
		{"heartbeat the run", func() error { return c.TouchRunHeartbeat(ctx, trig.ID) }},
		{"start the node", func() error { return c.StartNode(ctx, trig.ID, "build") }},
		{"annotate the node", func() error { return c.AppendNodeAnnotation(ctx, trig.ID, "build", "compiling") }},
		{"pause the node", func() error {
			return c.SetNodeStatus(ctx, trig.ID, "build", "paused")
		}},
		{"resume the node", func() error {
			return c.SetNodeStatus(ctx, trig.ID, "build", "running")
		}},
		{"finish the node", func() error {
			return c.FinishNode(ctx, trig.ID, "build", "success", "", nil)
		}},
		{"finish the run", func() error { return c.FinishRun(ctx, trig.ID, "success", "") }},
		{"finish the trigger", func() error { return c.FinishTrigger(ctx, trig.ID) }},
	} {
		if err := step.call(); err != nil {
			t.Fatalf("%s on the documented runner scope set: %v", step.name, err)
		}
	}

	run, err := f.store.GetRun(ctx, trig.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "success" {
		t.Errorf("run status = %q, want success", run.Status)
	}
	if run.Repo != "acme/web" {
		t.Errorf("run repo = %q, want the trigger's acme/web", run.Repo)
	}
}

func TestRunnerScopes_PoolRunnerReadsItsRepositorySecret(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)
	seedRepoTrigger(t, f.store, "run-web", "acme/web")
	seedRunNode(t, f.store, "run-web", "build")
	setRunRepo(t, f.store, "run-web", "acme/web")
	if err := f.store.MarkNodeReady(ctx, "run-web", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if _, err := c.GetRunForExecution(ctx, "run-web"); err != nil {
		t.Fatalf("GetRunForExecution while holding the node claim: %v", err)
	}
	if _, err := c.GetTrigger(ctx, "run-web"); err != nil {
		t.Fatalf("GetTrigger while holding the node claim: %v", err)
	}

	sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", "run-web")
	if err != nil {
		t.Fatalf("GetSecretForRun while holding the claim: %v", err)
	}
	if sec.Value != "web-key" {
		t.Errorf("GetSecretForRun value = %q, want the claimed run's repository row", sec.Value)
	}
}

func setRunRepo(t *testing.T, st *store.Store, runID, repo string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE runs SET repo = ? WHERE id = ?`, repo, runID); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerScopes_SecretsReadIsNotAdmin(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()

	seedSecret(t, f.store, "DEPLOY_KEY", "api-key", "acme/api", false)
	seedRunNode(t, f.store, "run-web", "build")
	setRunRepo(t, f.store, "run-web", "acme/web")
	if err := f.store.MarkNodeReady(ctx, "run-web", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewWithToken(f.url, nil, raw).
		ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	t.Run("cannot read another repository's secret", func(t *testing.T) {
		c := client.NewWithToken(f.url, nil, raw)
		if _, err := c.GetSecretForRepo(ctx, "DEPLOY_KEY", "acme/api"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetSecretForRepo(acme/api) err = %v, want ErrNotFound; the caller named a repo it does not hold", err)
		}
	})

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"mint a token", http.MethodPost, "/api/v1/tokens", `{"principal":"mallory","kind":"user","scopes":["admin"]}`},
		{"list tokens", http.MethodGet, "/api/v1/tokens", ""},
		{"read users", http.MethodGet, "/api/v1/users", ""},
		{"create a user", http.MethodPost, "/api/v1/users", `{"name":"mallory","password":"hunter2hunter2"}`},
		{"list every secret", http.MethodGet, "/api/v1/secrets", ""},
		{"write a secret", http.MethodPost, "/api/v1/secrets", `{"name":"X","value":"y"}`},
		{"mark a node ready", http.MethodPost, "/api/v1/runs/run-web/nodes/build/mark-ready", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, raw, tc.method, tc.path, tc.body); got != http.StatusForbidden {
				t.Errorf("%s %s = %d, want 403", tc.method, tc.path, got)
			}
		})
	}
}

// A runner cannot repoint its own run at another repository and then
// read that repository's credential.
func TestRunnerScopes_RunRepositoryComesFromTheTrigger(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-web", "acme/web")
	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)
	seedSecret(t, f.store, "DEPLOY_KEY", "api-key", "acme/api", false)

	trig, err := c.ClaimTrigger(ctx)
	if err != nil || trig == nil {
		t.Fatalf("ClaimTrigger: %v (trigger %v)", err, trig)
	}
	if err := c.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := c.FinishRun(ctx, "run-web", "pending", ""); err != nil {
		t.Fatalf("FinishRun(pending): %v", err)
	}
	if err := c.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy", Status: "running",
		Repo: "acme/api", StartedAt: time.Now().UTC(),
	}); err == nil {
		t.Error("CreateRun with a repo the trigger does not name succeeded, want a 400")
	}
	if err := c.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun replay: %v", err)
	}

	run, err := f.store.GetRun(ctx, "run-web")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Repo != "acme/web" {
		t.Fatalf("run repo = %q, want acme/web; the runner rewrote it", run.Repo)
	}
	sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", "run-web")
	if err != nil {
		t.Fatalf("GetSecretForRun: %v", err)
	}
	if sec.Value != "web-key" {
		t.Errorf("GetSecretForRun value = %q, want web-key", sec.Value)
	}
}

// One pool token holds claims in two repositories at once, so the read
// has to name the run it is for instead of picking one.
func TestRunnerScopes_SecretReadNamesItsRun(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)
	seedSecret(t, f.store, "DEPLOY_KEY", "api-key", "acme/api", false)
	for _, seed := range []struct{ run, repo string }{
		{"run-web", "acme/web"},
		{"run-api", "acme/api"},
	} {
		seedRunNode(t, f.store, seed.run, "build")
		setRunRepo(t, f.store, seed.run, seed.repo)
		if err := f.store.MarkNodeReady(ctx, seed.run, "build"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ClaimNode(ctx, "holder-"+seed.run, nil, time.Minute, nil); err != nil {
			t.Fatalf("ClaimNode %s: %v", seed.run, err)
		}
	}

	if _, err := c.GetSecret(ctx, "DEPLOY_KEY"); err == nil {
		t.Error("GetSecret with claims in two repositories succeeded, want a refusal naming ?run")
	}
	for _, tc := range []struct{ run, want string }{
		{"run-web", "web-key"},
		{"run-api", "api-key"},
	} {
		sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", tc.run)
		if err != nil {
			t.Fatalf("GetSecretForRun(%s): %v", tc.run, err)
		}
		if sec.Value != tc.want {
			t.Errorf("GetSecretForRun(%s) = %q, want %q", tc.run, sec.Value, tc.want)
		}
	}
}

func TestRunnerScopes_UnscopedSecretNeedsShared(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "LEGACY_KEY", "legacy", "", false)
	seedSecret(t, f.store, "NPM_TOKEN", "npm", "", true)
	seedRunNode(t, f.store, "run-web", "build")
	setRunRepo(t, f.store, "run-web", "acme/web")
	if err := f.store.MarkNodeReady(ctx, "run-web", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	if _, err := c.GetSecretForRun(ctx, "LEGACY_KEY", "run-web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSecretForRun(LEGACY_KEY) err = %v, want ErrNotFound until an admin shares it", err)
	}
	sec, err := c.GetSecretForRun(ctx, "NPM_TOKEN", "run-web")
	if err != nil {
		t.Fatalf("GetSecretForRun(NPM_TOKEN): %v", err)
	}
	if sec.Value != "npm" {
		t.Errorf("GetSecretForRun(NPM_TOKEN) = %q, want npm", sec.Value)
	}
	if got := f.do(t, raw, http.MethodGet, "/api/v1/secrets/LEGACY_KEY", ""); got != http.StatusNotFound {
		t.Errorf("GET LEGACY_KEY = %d, want 404", got)
	}
}

func TestRunnerScopes_StateWritesStillNeedTheNodeClaim(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()

	stranger, _, err := f.store.CreateToken("pool-b", store.TokenKindRunner,
		runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	seedRunNode(t, f.store, "run-1", "build")
	if err := f.store.MarkNodeReady(ctx, "run-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewWithToken(f.url, nil, raw).
		ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	for _, path := range []string{
		"/api/v1/runs/run-1?include=secret_values",
		"/api/v1/triggers/run-1",
	} {
		if got := f.do(t, stranger, http.MethodGet, path, ""); got != http.StatusForbidden {
			t.Errorf("stranger GET %s = %d, want 403", path, got)
		}
	}

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"start a node", "/api/v1/runs/run-1/nodes/build/start", `{}`},
		{"finish a node", "/api/v1/runs/run-1/nodes/build/finish", `{"outcome":"success"}`},
		{"finish the run", "/api/v1/runs/run-1/finish", `{"status":"success"}`},
		{"forge an event", "/api/v1/runs/run-1/events", `{"node_id":"build","kind":"node_failed"}`},
		{"inject a node", "/api/v1/runs/run-1/nodes", `{"run_id":"run-1","id":"backdoor","status":"pending"}`},
		{"rewrite the plan", "/api/v1/runs/run-1/plan", `{}`},
		{"upsert the run", "/api/v1/runs", `{"id":"run-1","pipeline":"demo","status":"running"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, stranger, http.MethodPost, tc.path, tc.body); got != http.StatusForbidden {
				t.Errorf("stranger POST %s = %d, want 403", tc.path, got)
			}
		})
	}
}

func (f scopedFixture) do(t *testing.T, token, method, path, body string) int {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
