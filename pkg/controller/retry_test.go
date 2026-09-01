package controller_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRetry_CreatesNewTriggerWithSameInputs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	src := store.Run{
		ID:        "src-run",
		Pipeline:  "deploy",
		Args:      map[string]string{"env": "prod", "tag": "v1"},
		Status:    "failed",
		GitBranch: "main",
		GitSHA:    "abc123",
		Repo:      "owner/repo-a",
		RepoURL:   "git@example.test:owner/repo-a.git",
		Invocation: map[string]any{
			"cwd": filepath.Join(dir, "repo-a"),
		},
		PlanSnapshot: []byte(`{"pipeline":"deploy","run_id":"src-run","nodes":[{"id":"deploy","deps":[]}]}`),
		StartedAt:    time.Now().Add(-5 * time.Minute),
	}
	if err := st.CreateRun(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, src.ID, "failed", "exit 1"); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/runs/"+src.ID+"/retry", "application/json", bytes.NewBufferString(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	newID, _ := body["id"].(string)
	if newID == "" || newID == src.ID {
		t.Fatalf("expected a fresh run id, got %q", newID)
	}
	if body["pipeline"] != src.Pipeline {
		t.Errorf("pipeline=%v want %s", body["pipeline"], src.Pipeline)
	}
	if body["retry_of"] != src.ID {
		t.Errorf("retry_of=%v want %s", body["retry_of"], src.ID)
	}

	trig, err := st.GetTrigger(ctx, newID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.Pipeline != src.Pipeline {
		t.Errorf("trigger pipeline=%s want %s", trig.Pipeline, src.Pipeline)
	}
	if trig.Args["env"] != "prod" || trig.Args["tag"] != "v1" {
		t.Errorf("trigger args didn't copy: %+v", trig.Args)
	}
	if trig.TriggerSource != "retry" {
		t.Errorf("trigger source=%s want retry", trig.TriggerSource)
	}
	if trig.GitSHA != src.GitSHA {
		t.Errorf("git_sha=%s want %s", trig.GitSHA, src.GitSHA)
	}
	if got := trig.TriggerEnv[retryprovenance.RepoDirKey]; got != filepath.Join(dir, "repo-a") {
		t.Errorf("retry repo dir=%q want %q", got, filepath.Join(dir, "repo-a"))
	}
	if got := trig.TriggerEnv[retryprovenance.RepoIdentityKey]; got != src.RepoURL {
		t.Errorf("retry repo identity=%q want %q", got, src.RepoURL)
	}
	if got := trig.TriggerEnv[retryprovenance.RevisionKey]; got != src.GitSHA {
		t.Errorf("retry revision=%q want %q", got, src.GitSHA)
	}
	sum := sha256.Sum256(src.PlanSnapshot)
	if got, want := trig.TriggerEnv[retryprovenance.PlanHashKey], fmt.Sprintf("sha256:%x", sum); got != want {
		t.Errorf("retry plan hash=%q want %q", got, want)
	}
}

func TestRetry_WorkingTreeRunRetainsDesktopClaimSource(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	source := "pipeline-working-tree@moonborn.local"
	if err := st.CreateRun(ctx, store.Run{
		ID: "workspace-run", Pipeline: "build", Status: "failed", TriggerSource: source,
		GitSHA: strings.Repeat("a", 40), RepoURL: "https://git.example.com/acme/widgets.git", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/runs/workspace-run/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	id, _ := body["id"].(string)
	trigger, err := st.GetTrigger(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.TriggerSource != source || body["trigger_source"] != source {
		t.Fatalf("retry source = trigger %q response %v, want %q", trigger.TriggerSource, body["trigger_source"], source)
	}
	if trigger.RetryOf != "workspace-run" {
		t.Fatalf("retry_of = %q", trigger.RetryOf)
	}
}

func TestRetry_OfRetryInheritsOriginalCheckoutInsteadOfEphemeralSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	originalDir := filepath.Join(dir, "original-checkout")
	src := store.Run{
		ID:           "retry-attempt",
		Pipeline:     "deploy",
		Status:       "failed",
		GitSHA:       "recorded-sha",
		RepoURL:      "git@example.test:owner/repo.git",
		PlanSnapshot: []byte(`{"pipeline":"deploy","nodes":[{"id":"deploy"}]}`),
		Invocation: map[string]any{
			"cwd": filepath.Join(dir, "deleted-sparkwing-retry-snapshot"),
			"retry_provenance": map[string]any{
				"repo_dir":       originalDir,
				"repo_identity":  "git@example.test:owner/repo.git",
				"revision":       "recorded-sha",
				"plan_hash":      "sha256:prior-attempt-plan",
				"content_policy": retryprovenance.RecordedRevisionSnapshotPolicy,
			},
		},
		StartedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, src); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/runs/"+src.ID+"/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.GetTrigger(ctx, body["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if got := trigger.TriggerEnv[retryprovenance.RepoDirKey]; got != originalDir {
		t.Fatalf("retry chain repo dir=%q, want durable original %q", got, originalDir)
	}
	if got := trigger.TriggerEnv[retryprovenance.RepoIdentityKey]; got != "git@example.test:owner/repo.git" {
		t.Fatalf("retry chain repo identity=%q", got)
	}
	if got := trigger.TriggerEnv[retryprovenance.RevisionKey]; got != "recorded-sha" {
		t.Fatalf("retry chain revision=%q", got)
	}
	sum := sha256.Sum256(src.PlanSnapshot)
	if got, want := trigger.TriggerEnv[retryprovenance.PlanHashKey], fmt.Sprintf("sha256:%x", sum); got != want {
		t.Fatalf("retry chain plan hash=%q want %q", got, want)
	}
}

func TestRetry_PreAllocatesPendingRunRow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	src := store.Run{
		ID:        "src-pre",
		Pipeline:  "deploy",
		Status:    "failed",
		StartedAt: time.Now().Add(-5 * time.Minute),
	}
	if err := st.CreateRun(ctx, src); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/runs/"+src.ID+"/retry", "application/json", bytes.NewBufferString(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "pending" {
		t.Errorf("body.status=%v want pending", body["status"])
	}
	if body["trigger_source"] != "retry" {
		t.Errorf("body.trigger_source=%v want retry", body["trigger_source"])
	}
	if _, leaked := body["trigger"]; leaked {
		t.Errorf("body leaked legacy 'trigger' field: %v", body)
	}
	newID, _ := body["id"].(string)
	if newID == "" {
		t.Fatal("empty id in response")
	}

	run, err := st.GetRun(ctx, newID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", newID, err)
	}
	if run.Status != "pending" {
		t.Errorf("Run.Status=%q want pending", run.Status)
	}
	if run.TriggerSource != "retry" {
		t.Errorf("Run.TriggerSource=%q want retry", run.TriggerSource)
	}
	if run.RetryOf != src.ID {
		t.Errorf("Run.RetryOf=%q want %q", run.RetryOf, src.ID)
	}
}

func TestRetry_FullQueryParam(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "src-full", Pipeline: "p", Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/runs/src-full/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var body1 map[string]any
	resp2, err := http.Post(srv.URL+"/api/v1/runs/src-full/retry?full=1", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if err := json.NewDecoder(resp2.Body).Decode(&body1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	fullID, _ := body1["id"].(string)
	trig, err := st.GetTrigger(ctx, fullID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if !trig.Full {
		t.Errorf("Full=%v want true (?full=1)", trig.Full)
	}
}

func TestRetry_ListAttempts(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	t0 := time.Now().Add(-3 * time.Hour)
	if err := st.CreateRun(ctx, store.Run{
		ID: "root", Pipeline: "p", Status: "failed", CreatedAt: t0, StartedAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "second", Pipeline: "p", Status: "failed",
		RetryOf: "root", CreatedAt: t0.Add(time.Hour), StartedAt: t0.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/runs/root/attempts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var body struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Runs) != 2 {
		t.Fatalf("runs=%d want 2", len(body.Runs))
	}
	if body.Runs[0]["id"] != "root" || body.Runs[1]["id"] != "second" {
		t.Errorf("order=[%v,%v] want [root,second]", body.Runs[0]["id"], body.Runs[1]["id"])
	}
}

func TestRetry_UnknownRunReturns404(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "s.db"))
	defer func() { _ = st.Close() }()
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/runs/missing/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}
