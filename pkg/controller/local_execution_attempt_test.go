package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func localAttemptStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestExecutionAttempt_ClusterControllerRefusesALocalAttempt(t *testing.T) {
	st := localAttemptStore(t)
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	status, body := postJSONWithStatus(t, srv.URL+"/api/v1/runs/run-1/nodes/build/execution-start",
		map[string]any{"attempt_ordinal": 1, "executor_kind": "local", "executor_id": "attacker"})
	if status != http.StatusBadRequest ||
		!strings.Contains(body, "accepted only by a host's own admission daemon") {
		t.Fatalf("execution-start status=%d body=%s, want 400 refusing a forged local attempt", status, body)
	}
	status, body = postJSONWithStatus(t, srv.URL+"/api/v1/runs/run-1/nodes/build/execution-finish",
		map[string]any{"attempt_ordinal": 1, "outcome": "success", "executor_kind": "local"})
	if status != http.StatusBadRequest ||
		!strings.Contains(body, "accepted only by a host's own admission daemon") {
		t.Fatalf("execution-finish status=%d body=%s, want 400 refusing a forged local attempt", status, body)
	}
	attempts, err := st.ListNodeExecutionAttempts(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatalf("attempts = %+v, want none recorded", attempts)
	}
}

func TestExecutionAttempt_LocalDaemonAcceptsALocalAttempt(t *testing.T) {
	st := localAttemptStore(t)
	srv := httptest.NewServer(controller.New(st, nil).WithLocalExecution().Handler())
	defer srv.Close()

	status, body := postJSONWithStatus(t, srv.URL+"/api/v1/runs/run-1/nodes/build/execution-start",
		map[string]any{"attempt_ordinal": 1, "executor_kind": "local", "executor_id": "workstation-1"})
	if status != http.StatusNoContent {
		t.Fatalf("execution-start status=%d body=%s, want 204", status, body)
	}
	status, body = postJSONWithStatus(t, srv.URL+"/api/v1/runs/run-1/nodes/build/execution-finish",
		map[string]any{"attempt_ordinal": 1, "outcome": "success", "executor_kind": "local"})
	if status != http.StatusNoContent {
		t.Fatalf("execution-finish status=%d body=%s, want 204", status, body)
	}

	attempts, err := st.ListNodeExecutionAttempts(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	got := attempts[0]
	if got.Attempt != 1 || got.ExecutorKind != store.ExecutorKindLocal ||
		got.ExecutorName != "workstation-1" || got.ExecutorLocation != "local" ||
		got.Outcome != "success" {
		t.Fatalf("attempt = %+v, want attempt 1 on local/workstation-1 with outcome success", got)
	}
}

func TestExecutionAttempt_LocalDaemonStillRequiresAnExecutorID(t *testing.T) {
	st := localAttemptStore(t)
	srv := httptest.NewServer(controller.New(st, nil).WithLocalExecution().Handler())
	defer srv.Close()

	status, body := postJSONWithStatus(t, srv.URL+"/api/v1/runs/run-1/nodes/build/execution-start",
		map[string]any{"attempt_ordinal": 1, "executor_kind": "local"})
	if status != http.StatusBadRequest || !strings.Contains(body, "executor_id") {
		t.Fatalf("execution-start status=%d body=%s, want 400 naming executor_id", status, body)
	}
}
