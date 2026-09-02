package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/inprocdispatch"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTrigger_Validation(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp2 := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"unknown":  true,
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field status=%d want 400", resp2.StatusCode)
	}
	_ = resp2.Body.Close()
}

func TestTrigger_MissingSource400(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (missing trigger.source)", resp.StatusCode)
	}
}

func TestTrigger_NoopDispatcher(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger":  map[string]string{"source": "github"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RunID == "" {
		t.Error("run_id empty")
	}
	if body.Status != "dispatched" {
		t.Errorf("status=%q want dispatched", body.Status)
	}
}

func TestTrigger_StripsClientSuppliedLeaseTokenEnv(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger": map[string]any{
			"source": "manual",
			"env": map[string]string{
				"SPARKWING_LEASE_TOKEN":       "stale-local-token",
				"SPARKWING_CHILD_LEASE_TOKEN": "stale-child-token",
				"GITHUB_REPOSITORY":           "sparkwing-dev/sparkwing",
			},
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	trigger, err := st.GetTrigger(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["SPARKWING_LEASE_TOKEN"] != "" {
		t.Fatalf("local lease token persisted from public env: %+v", trigger.TriggerEnv)
	}
	if trigger.TriggerEnv["SPARKWING_CHILD_LEASE_TOKEN"] != "" {
		t.Fatalf("child lease token persisted from public env: %+v", trigger.TriggerEnv)
	}
	if trigger.TriggerEnv["GITHUB_REPOSITORY"] != "sparkwing-dev/sparkwing" {
		t.Fatalf("allow-listed env = %q, want sparkwing-dev/sparkwing", trigger.TriggerEnv["GITHUB_REPOSITORY"])
	}
}

func TestTrigger_DropsEnvKeysOutsideTheAllowList(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger": map[string]any{
			"source": "manual",
			"env": map[string]string{
				"SPARKWING_LEASE_TOKEN":         "stale-local-token",
				"NPM_TOKEN":                     "npm-bearer",
				"SPARKWING_PG_URL":              "postgres://sparkwing:hunter2@db.example/sparkwing",
				"GITHUB_REPOSITORY":             "sparkwing-dev/sparkwing",
				retryprovenance.RepoDirKey:      "/src/sparkwing",
				retryprovenance.RevisionKey:     "deadbeef",
				retryprovenance.PlanHashKey:     "plan-1",
				retryprovenance.RepoIdentityKey: "github.com/sparkwing-dev/sparkwing",
			},
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()

	trigger, err := st.GetTrigger(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	for _, key := range []string{
		"SPARKWING_LEASE_TOKEN", "NPM_TOKEN", "SPARKWING_PG_URL",
		retryprovenance.RepoDirKey, retryprovenance.RevisionKey,
		retryprovenance.PlanHashKey, retryprovenance.RepoIdentityKey,
	} {
		if _, ok := trigger.TriggerEnv[key]; ok {
			t.Fatalf("%s persisted in trigger_env: %+v", key, trigger.TriggerEnv)
		}
	}
	for key, want := range map[string]string{
		"GITHUB_REPOSITORY": "sparkwing-dev/sparkwing",
	} {
		if trigger.TriggerEnv[key] != want {
			t.Fatalf("%s = %q, want %q", key, trigger.TriggerEnv[key], want)
		}
	}
}

func TestTrigger_InProcessDispatcher_FullLoop(t *testing.T) {
	registerPipeline("trigger-e2e", func() sparkwing.Pipeline[sparkwing.NoInputs] { return triggerE2EPipe{} })

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	local := orchestrator.LocalBackends(paths, st, nil)
	backends := orchestrator.Backends{
		State:       client.New(ts.URL, nil),
		Logs:        local.Logs,
		Concurrency: local.Concurrency,
	}
	srv.WithDispatcher(inprocdispatch.InProcessDispatcher{Backends: backends})

	resp := postJSON(t, ts.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "trigger-e2e",
		"trigger":  map[string]string{"source": "github"},
		"git":      map[string]string{"branch": "main", "sha": "abc123"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("trigger status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.RunID == "" {
		t.Fatal("empty run_id")
	}

	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var finalRun *store.Run
	for finalRun == nil {
		run, err := st.GetRun(context.Background(), body.RunID)
		switch {
		case err == nil && run.FinishedAt != nil:
			finalRun = run
		case err == nil:
		case errors.Is(err, store.ErrNotFound):
		default:
			t.Fatalf("GetRun: %v", err)
		}
		if finalRun != nil {
			continue
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("run %s never finished within deadline", body.RunID)
		}
	}
	if finalRun.Status != "success" {
		t.Errorf("run status=%q want success (err=%q)", finalRun.Status, finalRun.Error)
	}
	if finalRun.TriggerSource != "github" {
		t.Errorf("trigger_source=%q want github", finalRun.TriggerSource)
	}

	nodes, err := st.ListNodes(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Errorf("nodes=%d want 1", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Success) {
		t.Errorf("node outcome=%q want success", nodes[0].Outcome)
	}
}

func TestTrigger_CreatesPendingRunRow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger":  map[string]string{"source": "github"},
		"git":      map[string]string{"branch": "main", "sha": "deadbeef"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RunID == "" {
		t.Fatal("empty run_id")
	}

	run, err := st.GetRun(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", body.RunID, err)
	}
	if run.Status != "pending" {
		t.Errorf("Status=%q want pending", run.Status)
	}
	if run.Pipeline != "demo" {
		t.Errorf("Pipeline=%q want demo", run.Pipeline)
	}
	if run.GitSHA != "deadbeef" {
		t.Errorf("GitSHA=%q want deadbeef", run.GitSHA)
	}
	if run.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	runs, err := st.ListRuns(context.Background(), store.RunFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	found := false
	for _, r := range runs {
		if r.ID == body.RunID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListRuns did not include the pending run %s", body.RunID)
	}
}

func TestTrigger_PendingTransitionsToRunning(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	created := time.Now().Add(-time.Hour)
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-pending-1",
		Pipeline:  "demo",
		Status:    "pending",
		CreatedAt: created,
		StartedAt: created,
	}); err != nil {
		t.Fatalf("CreateRun pending: %v", err)
	}

	started := time.Now()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-pending-1",
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun running upsert: %v", err)
	}

	got, err := st.GetRun(ctx, "run-pending-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("Status=%q want running", got.Status)
	}
	if got.CreatedAt.Truncate(time.Second) != created.Truncate(time.Second) {
		t.Errorf("CreatedAt=%v want %v (lost on upsert)", got.CreatedAt, created)
	}
}

func TestTrigger_DispatcherError(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	srv.WithDispatcher(&failingDispatcher{})

	resp := postJSON(t, ts.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "x",
		"trigger":  map[string]string{"source": "manual"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d want 500", resp.StatusCode)
	}
}

type triggerE2EPipe struct{ sparkwing.Base }

func (triggerE2EPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "work", func(ctx context.Context) error {
		sparkwing.Info(ctx, "work via webhook trigger")
		return nil
	})
	return nil
}

type failingDispatcher struct {
	called atomic.Int32
}

func (f *failingDispatcher) Dispatch(_ context.Context, _ controller.RunRequest) error {
	f.called.Add(1)
	return errors.New("dispatcher broken")
}

type captureDispatcher struct {
	mu   sync.Mutex
	last controller.RunRequest
}

func (c *captureDispatcher) Dispatch(_ context.Context, req controller.RunRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = req
	return nil
}

var registerOnce sync.Map

func registerPipeline(name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) {
	if _, loaded := registerOnce.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestTrigger_RequiresJSONContentType(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srvController := controller.New(st, nil)
	srvController.WithDispatcher(&captureDispatcher{})
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	cases := []struct {
		name        string
		contentType string
		wantStatus  int
	}{
		{name: "simple cross-site post", contentType: "text/plain;charset=UTF-8", wantStatus: http.StatusBadRequest},
		{name: "form post", contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusBadRequest},
		{name: "absent", contentType: "", wantStatus: http.StatusBadRequest},
		{name: "json", contentType: "application/json", wantStatus: http.StatusAccepted},
		{name: "json with charset", contentType: "application/json; charset=utf-8", wantStatus: http.StatusAccepted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"pipeline":"demo","trigger":{"source":"test"}}`
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/triggers", bytes.NewReader([]byte(body)))
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d body = %q, want %d", resp.StatusCode, raw, tc.wantStatus)
			}
		})
	}
}

func TestTrigger_ValidatesRepoURL(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srvController := controller.New(st, nil)
	srvController.WithDispatcher(&captureDispatcher{})
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	for _, tc := range []struct {
		name    string
		repoURL string
		want    int
	}{
		{name: "https", repoURL: "https://github.com/sparkwing-dev/sparkwing.git", want: http.StatusAccepted},
		{name: "scp-like ssh", repoURL: "git@github.com:sparkwing-dev/sparkwing.git", want: http.StatusAccepted},
		{name: "empty", repoURL: "", want: http.StatusAccepted},
		{name: "local path", repoURL: "/tmp/repo", want: http.StatusBadRequest},
		{name: "file scheme", repoURL: "file:///tmp/repo", want: http.StatusBadRequest},
		{name: "loopback host", repoURL: "https://127.0.0.1/repo.git", want: http.StatusBadRequest},
		{name: "embedded credentials", repoURL: "https://user:secret@example.com/repo.git", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
				"pipeline": "demo",
				"trigger":  map[string]any{"source": "manual"},
				"git":      map[string]any{"repo_url": tc.repoURL},
			})
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body: %s)", resp.StatusCode, tc.want, body)
			}
		})
	}
}

func TestTrigger_RequiresAGitHubRepositorySlug(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srvController := controller.New(st, nil)
	srvController.WithDispatcher(&captureDispatcher{})
	srv := httptest.NewServer(srvController.Handler())
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name string
		repo string
		want int
	}{
		{name: "slug", repo: "sparkwing-dev/sparkwing", want: http.StatusAccepted},
		{name: "absent", repo: "", want: http.StatusAccepted},
		{name: "https URL", repo: "https://attacker.example/payload.git", want: http.StatusBadRequest},
		{name: "scp-like URL", repo: "git@attacker.example:payload.git", want: http.StatusBadRequest},
		{name: "three segments", repo: "owner/name/extra", want: http.StatusBadRequest},
		{name: "no slash", repo: "sparkwing", want: http.StatusBadRequest},
		{name: "space", repo: "owner/na me", want: http.StatusBadRequest},
		{name: "non-ascii", repo: "ownér/name", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			if tc.repo != "" {
				env["GITHUB_REPOSITORY"] = tc.repo
			}
			resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
				"pipeline": "demo",
				"trigger":  map[string]any{"source": "manual", "env": env},
			})
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body: %s)", resp.StatusCode, tc.want, body)
			}
		})
	}
}

func TestTrigger_ReservesGitHubProvenanceForTheWebhook(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	submitter, _, err := st.CreateToken("ci", store.TokenKindService,
		[]string{controller.ScopeRunsWrite}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	operator, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srvController := controller.New(st, nil).EnableAuthFromStore()
	srvController.WithDispatcher(&captureDispatcher{})
	srv := httptest.NewServer(srvController.Handler())
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name  string
		token string
		body  map[string]any
		want  int
	}{
		{
			name:  "runs.write cannot claim the github source",
			token: submitter,
			body: map[string]any{
				"pipeline": "demo",
				"trigger":  map[string]any{"source": "github"},
				"git":      map[string]any{"github_owner": "acme", "github_repo": "widgets"},
			},
			want: http.StatusForbidden,
		},
		{
			name:  "runs.write cannot carry pull-request provenance",
			token: submitter,
			body: map[string]any{
				"pipeline": "demo",
				"trigger": map[string]any{"source": "manual", "env": map[string]string{
					sparkwing.EnvPRHeadSHA: "deadbeef",
				}},
			},
			want: http.StatusForbidden,
		},
		{
			name:  "runs.write submits its own source",
			token: submitter,
			body: map[string]any{
				"pipeline": "demo",
				"trigger":  map[string]any{"source": "manual"},
			},
			want: http.StatusAccepted,
		},
		{
			name:  "admin still speaks for the webhook",
			token: operator,
			body: map[string]any{
				"pipeline": "demo",
				"trigger": map[string]any{"source": "github", "env": map[string]string{
					sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest,
					sparkwing.EnvPRHeadSHA:       "deadbeef",
				}},
				"git": map[string]any{"github_owner": "acme", "github_repo": "widgets"},
			},
			want: http.StatusAccepted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postJSONWithBearer(t, srv.URL+"/api/v1/triggers", tc.token, tc.body)
			if status != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", status, tc.want, body)
			}
		})
	}
}

func TestListRoutes_ClampTheirLimit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "push", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for i := range store.MaxRunListLimit + 5 {
		if err := st.CreateTrigger(ctx, store.Trigger{
			ID:        "tg-" + strconv.Itoa(i),
			Pipeline:  "push",
			CreatedAt: time.Unix(int64(i+1), 0),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendEvent(ctx, "run-1", "node-1", "log", []byte(`{"line":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)

	var triggers struct {
		Triggers []json.RawMessage `json:"triggers"`
	}
	getJSON(t, srv.URL+"/api/v1/triggers?limit=1000000000", &triggers)
	if len(triggers.Triggers) != store.MaxRunListLimit {
		t.Fatalf("triggers = %d, want %d", len(triggers.Triggers), store.MaxRunListLimit)
	}

	var events []json.RawMessage
	getJSON(t, srv.URL+"/api/v1/runs/run-1/events?limit=1000000000", &events)
	if len(events) != store.MaxRunListLimit {
		t.Fatalf("events = %d, want %d", len(events), store.MaxRunListLimit)
	}
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status = %d (body: %s)", url, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}
