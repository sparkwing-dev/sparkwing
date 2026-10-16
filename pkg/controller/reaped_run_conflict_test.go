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
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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

func TestCreateNode_LiveRunStillAcceptsNodes(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{
		ID: "run-live", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if status, message := postNode(t, srv.URL, "run-live", "node-a"); status != http.StatusCreated {
		t.Fatalf("status = %d (%s), want %d", status, message, http.StatusCreated)
	}
	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetNode(ctx, "run-live", "node-a"); err != nil {
		t.Fatalf("GetNode: %v", err)
	}
}

func TestCreateNode_StrangerTokenLearnsNothingAboutTheRun(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	const secret = "s3://acme-private/creds.json not readable by sa/acme-prod"
	if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{
		ID: "victim", Pipeline: "secret-pipeline", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, "victim", "failed", secret); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	raw, _, err := st.CreateToken("runner-stranger", store.TokenKindRunner,
		[]string{"runs.state", "nodes.claim"}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	payload, err := json.Marshal(store.Node{RunID: "victim", NodeID: "n1", Status: "pending"})
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/runs/victim/nodes", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("X-Sparkwing-Trigger-Generation", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST node: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	if body["error"] != store.ErrLockHeld.Error() {
		t.Errorf("stranger was answered %q, want the bare %q", body["error"], store.ErrLockHeld)
	}
	for _, leaked := range []string{secret, "failed", "secret-pipeline"} {
		if strings.Contains(body["error"], leaked) {
			t.Errorf("answer to a stranger carries %q: %q", leaked, body["error"])
		}
	}
	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetNode(ctx, "victim", "n1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stranger's node was written: %v", err)
	}
}
