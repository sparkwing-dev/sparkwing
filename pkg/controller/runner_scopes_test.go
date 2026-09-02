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

func TestRunnerScopes_DocumentedSetCompletesARunWithoutAdmin(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: "run-1", Pipeline: "deploy", Repo: "acme/web", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := f.store.CreateOrReplaceSecret(
		"DEPLOY_KEY", "web-key", "root", "acme/web", true, time.Now().UTC(),
	); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}

	trig, err := c.ClaimTrigger(ctx)
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if trig == nil {
		t.Fatal("ClaimTrigger returned no trigger; the queue was seeded with one")
	}
	if _, err := c.HeartbeatTrigger(ctx, trig.ID); err != nil {
		t.Fatalf("HeartbeatTrigger: %v", err)
	}

	if err := c.CreateRun(ctx, store.Run{
		ID: trig.ID, Pipeline: "deploy", Status: "running",
		Repo: "acme/web", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := c.CreateNode(ctx, store.Node{
		RunID: trig.ID, NodeID: "build", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := c.AppendEvent(ctx, trig.ID, "build", "node_queued", []byte(`{}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := f.store.MarkNodeReady(ctx, trig.ID, "build"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	claimed, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if claimed == nil || claimed.NodeID != "build" {
		t.Fatalf("ClaimNode = %+v, want node build", claimed)
	}
	if err := c.StartNode(ctx, trig.ID, "build"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}

	sec, err := c.GetSecret(ctx, "DEPLOY_KEY")
	if err != nil {
		t.Fatalf("GetSecret while holding the claim: %v", err)
	}
	if sec.Value != "web-key" {
		t.Errorf("GetSecret value = %q, want the claimed run's repository row", sec.Value)
	}

	if err := c.FinishNode(ctx, trig.ID, "build", "success", "", nil); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	if err := c.FinishRun(ctx, trig.ID, "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if err := c.FinishTrigger(ctx, trig.ID); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}

	run, err := f.store.GetRun(ctx, trig.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "success" {
		t.Errorf("run status = %q, want success", run.Status)
	}
}

func TestRunnerScopes_SecretsReadIsNotAdmin(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()

	if err := f.store.CreateOrReplaceSecret(
		"DEPLOY_KEY", "api-key", "root", "acme/api", true, time.Now().UTC(),
	); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}
	seedRunNode(t, f.store, "run-web", "build")
	if _, err := f.store.DB().ExecContext(ctx,
		`UPDATE runs SET repo = ? WHERE id = ?`, "acme/web", "run-web"); err != nil {
		t.Fatal(err)
	}
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, raw, tc.method, tc.path, tc.body); got != http.StatusForbidden {
				t.Errorf("%s %s = %d, want 403", tc.method, tc.path, got)
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

func TestRunnerScopes_StateWritesStillNeedTheNodeClaim(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()

	stranger, _, err := f.store.CreateToken("pool-b", store.TokenKindRunner,
		runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	seedRunNode(t, f.store, "run-1", "build")
	if err := f.store.MarkNodeReady(ctx, "run-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewWithToken(f.url, nil, raw).
		ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	for _, path := range []string{
		"/api/v1/runs/run-1/nodes/build/start",
		"/api/v1/runs/run-1/nodes/build/finish",
	} {
		if got := f.do(t, stranger, http.MethodPost, path, `{"outcome":"success"}`); got != http.StatusForbidden {
			t.Errorf("stranger POST %s = %d, want 403", path, got)
		}
	}
}
