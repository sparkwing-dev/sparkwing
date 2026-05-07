package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
)

// TestCycleDetect_RejectsSelfCycle: pipeline A's run spawns a trigger
// for A again. Controller detects the immediate self-cycle and 409s.
func TestCycleDetect_RejectsSelfCycle(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	// Seed: a parent run for pipeline "A".
	if err := st.CreateRun(ctx, store.Run{
		ID: "parent", Pipeline: "A", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	// Trigger for pipeline "A" carrying parent_run_id=parent -> cycle.
	body := strings.NewReader(`{"pipeline":"A","parent_run_id":"parent","trigger":{"source":"manual"}}`)
	resp, err := http.Post(srv.URL+"/api/v1/triggers", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 409 cycle, got %d: %s", resp.StatusCode, msg)
	}
	msg, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(msg), "cycle") {
		t.Fatalf("error body should mention cycle: %s", msg)
	}
}

// TestCycleDetect_AllowsIndirectNonCycle: a parent's ancestry of
// different pipeline names lets a fresh pipeline through.
func TestCycleDetect_AllowsIndirectNonCycle(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	// Chain: A -> B. Spawning C from B is fine.
	if err := st.CreateRun(ctx, store.Run{
		ID: "root", Pipeline: "A", Status: "success", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "mid", Pipeline: "B", Status: "running", StartedAt: time.Now(), ParentRunID: "root",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	body := bytes.NewReader([]byte(`{"pipeline":"C","parent_run_id":"mid","trigger":{"source":"manual"}}`))
	resp, err := http.Post(srv.URL+"/api/v1/triggers", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 202, got %d: %s", resp.StatusCode, msg)
	}
}

// TestCycleDetect_RejectsDeepCycle: A -> B -> C spawning A again
// re-enters. Reject even though the cycle is two hops away.
func TestCycleDetect_RejectsDeepCycle(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	for _, e := range []struct {
		id, pipeline, parent string
	}{
		{"r1", "A", ""},
		{"r2", "B", "r1"},
		{"r3", "C", "r2"},
	} {
		if err := st.CreateRun(ctx, store.Run{
			ID: e.id, Pipeline: e.pipeline, Status: "running",
			StartedAt: time.Now(), ParentRunID: e.parent,
		}); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	body := strings.NewReader(`{"pipeline":"A","parent_run_id":"r3","trigger":{"source":"manual"}}`)
	resp, err := http.Post(srv.URL+"/api/v1/triggers", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deep cycle: want 409, got %d", resp.StatusCode)
	}
}

// TestTrigger_ParentRepoInheritance: when a spawned trigger carries
// parent_run_id but no git fields, the controller copies the parent
// run's git context onto the persisted trigger. Without this,
// runners claiming the spawned trigger have no .git context and
// fall through to the BakedBinary path.
func TestTrigger_ParentRepoInheritance(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "parent", Pipeline: "build-cluster", Status: "running",
		StartedAt:   time.Now(),
		Repo:        "sample-app",
		RepoURL:     "git@github.com:acme/sample-app.git",
		GitBranch:   "main",
		GitSHA:      "abc123",
		GithubOwner: "acme",
		GithubRepo:  "sample-app",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	body := strings.NewReader(`{"pipeline":"build","parent_run_id":"parent","trigger":{"source":"manual"}}`)
	resp, err := http.Post(srv.URL+"/api/v1/triggers", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 202, got %d: %s", resp.StatusCode, msg)
	}
	var triggerResp struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&triggerResp); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetTrigger(ctx, triggerResp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "sample-app" {
		t.Errorf("Repo: got %q, want sample-app", got.Repo)
	}
	if got.RepoURL != "git@github.com:acme/sample-app.git" {
		t.Errorf("RepoURL: got %q", got.RepoURL)
	}
	if got.GitBranch != "main" {
		t.Errorf("GitBranch: got %q, want main", got.GitBranch)
	}
	if got.GitSHA != "abc123" {
		t.Errorf("GitSHA: got %q, want abc123", got.GitSHA)
	}
	if got.GithubOwner != "acme" {
		t.Errorf("GithubOwner: got %q", got.GithubOwner)
	}
	if got.GithubRepo != "sample-app" {
		t.Errorf("GithubRepo: got %q", got.GithubRepo)
	}
}

// TestTrigger_ParentRepoInheritance_RespectsExplicit: when the body
// already carries git fields (e.g. webhook intake forwards them on
// behalf of the user), the inheritance pass must NOT overwrite them.
func TestTrigger_ParentRepoInheritance_RespectsExplicit(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "parent", Pipeline: "build-cluster", Status: "running",
		StartedAt: time.Now(),
		Repo:      "parent-repo", GitBranch: "main", GitSHA: "parentSHA",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	body := strings.NewReader(`{
		"pipeline":"build",
		"parent_run_id":"parent",
		"trigger":{"source":"manual"},
		"git":{"repo":"explicit-repo","branch":"feature","sha":"explicitSHA"}
	}`)
	resp, err := http.Post(srv.URL+"/api/v1/triggers", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 202, got %d: %s", resp.StatusCode, msg)
	}
	var triggerResp struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&triggerResp); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetTrigger(ctx, triggerResp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "explicit-repo" {
		t.Errorf("Repo: explicit value clobbered: %q", got.Repo)
	}
	if got.GitBranch != "feature" {
		t.Errorf("GitBranch: explicit value clobbered: %q", got.GitBranch)
	}
	if got.GitSHA != "explicitSHA" {
		t.Errorf("GitSHA: explicit value clobbered: %q", got.GitSHA)
	}
}

// TestTrigger_CrossRepoAwait_DoesNotInheritParentSHA: when the caller
// declares a different repo via body.Git.Repo, the parent's SHA must
// NOT be copied -- it belongs to a different repo and the runner
// would fail with "fatal: not our ref" on fetch. SHA stays empty so
// the runner clones the branch tip.
func TestTrigger_CrossRepoAwait_DoesNotInheritParentSHA(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "parent", Pipeline: "build-cluster", Status: "running",
		StartedAt: time.Now(),
		Repo:      "acme/sample-app",
		RepoURL:   "git@github.com:acme/sample-app.git",
		GitBranch: "main",
		GitSHA:    "parentSHAofProduct",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	// Cross-repo spawn: declare a different repo, no SHA.
	body := strings.NewReader(`{
		"pipeline":"build",
		"parent_run_id":"parent",
		"trigger":{"source":"manual"},
		"git":{"repo":"acme/sample-app","branch":"main"}
	}`)
	resp, err := http.Post(srv.URL+"/api/v1/triggers", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 202, got %d: %s", resp.StatusCode, msg)
	}
	var triggerResp struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&triggerResp); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetTrigger(ctx, triggerResp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "acme/sample-app" {
		t.Errorf("Repo = %q, want acme/sample-app", got.Repo)
	}
	if got.GitSHA != "" {
		t.Errorf("GitSHA = %q, want empty (cross-repo must not inherit parent's SHA)", got.GitSHA)
	}
	if got.GitBranch != "main" {
		t.Errorf("GitBranch = %q, want main", got.GitBranch)
	}
}

// TestCycleDetect_ParentNotFound400: bogus parent_run_id gets a 400
// so the caller knows their trigger request is malformed rather than
// silently proceeding without the cycle check.
func TestCycleDetect_ParentNotFound400(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	body := strings.NewReader(`{"pipeline":"A","parent_run_id":"does-not-exist","trigger":{"source":"manual"}}`)
	resp, err := http.Post(srv.URL+"/api/v1/triggers", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}
