package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

const reapReason = "reaped: no run-level heartbeat for 3m; orchestrator is no longer running"

func postNode(t *testing.T, url, runID, nodeID string) (int, string) {
	t.Helper()
	body, err := json.Marshal(store.Node{RunID: runID, NodeID: nodeID, Status: "pending"})
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	resp, err := http.Post(url+"/api/v1/runs/"+runID+"/nodes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST node: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var payload map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp.StatusCode, payload["error"]
}

func TestCreateNode_ReapedRunAnswersConflictWithReason(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-reaped", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.FinishRun(ctx, "run-reaped", "failed", reapReason); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	status, message := postNode(t, srv.URL, "run-reaped", "node-a")
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d", status, http.StatusConflict)
	}
	if !strings.Contains(message, reapReason) {
		t.Errorf("conflict body %q does not carry the reason the run ended", message)
	}
	if !strings.Contains(message, "node-a") {
		t.Errorf("conflict body %q does not name the node it refused", message)
	}
	if _, err := st.GetNode(ctx, "run-reaped", "node-a"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("refused node was written anyway: %v", err)
	}

	err = client.New(srv.URL, nil).CreateNode(ctx, store.Node{
		RunID: "run-reaped", NodeID: "node-b", Status: "pending",
	})
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("child error = %v, want a conflict wrapping store.ErrLockHeld", err)
	}
	if !strings.Contains(err.Error(), reapReason) {
		t.Errorf("child error %q does not carry the reason the run ended", err)
	}
}

func TestCreateNode_LiveRunStillAcceptsNodes(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-live", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if status, message := postNode(t, srv.URL, "run-live", "node-a"); status != http.StatusCreated {
		t.Fatalf("status = %d (%s), want %d", status, message, http.StatusCreated)
	}
	if _, err := st.GetNode(ctx, "run-live", "node-a"); err != nil {
		t.Fatalf("GetNode: %v", err)
	}
}
