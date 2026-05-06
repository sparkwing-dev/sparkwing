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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestTrigger_Validation confirms malformed payloads fail fast
// without consulting the dispatcher.
func TestTrigger_Validation(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	// Empty pipeline.
	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}

	// Unknown field.
	resp2 := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"unknown":  true,
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field status=%d want 400", resp2.StatusCode)
	}
}

// A POST without trigger.source gets 400, not a 202 with a
// mislabeled default source.
func TestTrigger_MissingSource400(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		// trigger.source intentionally omitted
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (missing trigger.source)", resp.StatusCode)
	}
}

// TestTrigger_NoopDispatcher exercises the default path: controller
// accepts the trigger, returns a run_id, but no pipeline actually
// runs. Proves the handler returns quickly regardless of dispatch
// behavior.
func TestTrigger_NoopDispatcher(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger":  map[string]string{"source": "github"},
	})
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

// TestTrigger_InProcessDispatcher_FullLoop is the full vertical
// slice: webhook arrives, controller dispatches, pipeline runs
// against the same controller via HTTP, final state lands in the
// DB. Proves external triggers actually produce completed runs.
func TestTrigger_InProcessDispatcher_FullLoop(t *testing.T) {
	registerPipeline("trigger-e2e", func() sparkwing.Pipeline[sparkwing.NoInputs] { return triggerE2EPipe{} })

	// Build the server first; httptest gives us a URL; we can then
	// construct an InProcessDispatcher whose Backends point back at
	// that URL via the HTTP client.
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Local Logs + Locks; State is the HTTP client targeting the
	// same controller. This is the cluster-mode shape, collapsed
	// into one process.
	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	local := orchestrator.LocalBackends(paths, st) // State discarded
	backends := orchestrator.Backends{
		State:       client.New(ts.URL, nil),
		Logs:        local.Logs,
		Concurrency: local.Concurrency,
	}
	srv.WithDispatcher(controller.InProcessDispatcher{Backends: backends})

	// Fire the webhook.
	resp := postJSON(t, ts.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "trigger-e2e",
		"trigger":  map[string]string{"source": "github"},
		"git":      map[string]string{"branch": "main", "sha": "abc123"},
	})
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

	// Poll until the run terminates. Budget is generous; the
	// pipeline is trivial.
	deadline := time.Now().Add(3 * time.Second)
	var finalRun *store.Run
	for time.Now().Before(deadline) {
		run, err := st.GetRun(context.Background(), body.RunID)
		if err == nil && run.FinishedAt != nil {
			finalRun = run
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalRun == nil {
		t.Fatalf("run %s never finished within deadline", body.RunID)
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

// IMP-004: every accepted trigger creates a pending Run row so
// `runs list` / `runs status` show it before the runner has even
// claimed it. Without this, dispatches that fail at fetch / compile
// would never surface in the CLI.
func TestTrigger_CreatesPendingRunRow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger":  map[string]string{"source": "github"},
		"git":      map[string]string{"branch": "main", "sha": "deadbeef"},
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
	// runs list should include it.
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

// IMP-004: a controller-pre-allocated pending row gets transitioned
// to running when the orchestrator's CreateRun fires. This is the
// claimed -> running edge in the ticket. The upsert deliberately
// preserves the original CreatedAt so receipt fields (IMP-016) can
// reason about queue latency = StartedAt - CreatedAt.
func TestTrigger_PendingTransitionsToRunning(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

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

	// Orchestrator-side promotion: same id, status=running, fresh started_at.
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
	// CreatedAt preserved from original pending insert.
	if got.CreatedAt.Truncate(time.Second) != created.Truncate(time.Second) {
		t.Errorf("CreatedAt=%v want %v (lost on upsert)", got.CreatedAt, created)
	}
}

// TestTrigger_DispatcherError surfaces dispatcher-reported errors
// as 500 responses so the caller can retry.
func TestTrigger_DispatcherError(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	srv.WithDispatcher(&failingDispatcher{})

	resp := postJSON(t, ts.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "x",
		"trigger":  map[string]string{"source": "manual"},
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d want 500", resp.StatusCode)
	}
}

// --- fixtures ---

type triggerE2EPipe struct{ sparkwing.Base }

func (triggerE2EPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "work", sparkwing.JobFn(func(ctx context.Context) error {
		sparkwing.Info(ctx, "work via webhook trigger")
		return nil
	}))
	return nil
}

type failingDispatcher struct {
	called atomic.Int32
}

func (f *failingDispatcher) Dispatch(_ context.Context, _ controller.RunRequest) error {
	f.called.Add(1)
	return errors.New("dispatcher broken")
}

// registerPipeline defined in e2e_test context via the client package,
// but that's a different test package. Redefine locally.
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
