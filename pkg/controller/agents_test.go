package controller_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestAgents_DerivedFromClaims(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()

	// Seed a run with 2 nodes: one claimed by a pool pod (busy),
	// one by a laptop agent that finished (idle wrt active jobs).
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-a",
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "n2", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	_ = st.MarkNodeReady(ctx, "run-a", "n1")
	_ = st.MarkNodeReady(ctx, "run-a", "n2")
	if _, err := st.ClaimNextReadyNode(ctx, "runner:laptop-alice:1", 30*time.Second, nil); err != nil {
		t.Fatalf("claim1: %v", err)
	}
	if _, err := st.ClaimNextReadyNode(ctx, "pod:run-a:n-pool", 30*time.Second, nil); err != nil {
		t.Fatalf("claim2: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	data, err := httpGet(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Agents []controller.Agent `json:"agents"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, data)
	}
	if len(body.Agents) < 2 {
		t.Fatalf("expected 2 agents, got %d: %+v", len(body.Agents), body.Agents)
	}
	var sawAgent, sawPool bool
	for _, a := range body.Agents {
		if a.Type == "agent" && a.Name == "laptop-alice" {
			sawAgent = true
			if a.Status != "busy" {
				t.Errorf("laptop-alice status=%q want busy", a.Status)
			}
		}
		if a.Type == "pool" && a.Name == "run-a" {
			sawPool = true
		}
	}
	if !sawAgent {
		t.Errorf("missing laptop-alice agent: %+v", body.Agents)
	}
	if !sawPool {
		t.Errorf("missing pool pod: %+v", body.Agents)
	}
}

func TestAgents_EmptyWhenNoClaims(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	data, err := httpGet(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Agents []controller.Agent `json:"agents"`
	}
	_ = json.Unmarshal(data, &body)
	if len(body.Agents) != 0 {
		t.Fatalf("expected 0 agents, got %d", len(body.Agents))
	}
}
