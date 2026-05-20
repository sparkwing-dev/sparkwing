package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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
		StartedAt: time.Now().Add(-5 * time.Minute),
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

	// Confirm the trigger row landed with matching args / git.
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
}

// Retry pre-allocates a pending Run row at intake so the dashboard's
// runs list surfaces the attempt instantly. Response body is the
// canonical Run-shape (status=pending, trigger_source=retry).
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

	// Pre-allocated Run row visible immediately.
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

// ?full=1 sets the trigger's Full flag so the orchestrator skips
// rehydration and re-runs every node.
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

// /api/v1/runs/{id}/attempts returns every run in the retry tree
// rooted at the requested id, ordered by created_at.
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
