package controller_test

import (
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

func TestGetRun_IncludeNodes_NoPlanSnapshot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	seedRunNode(t, st, "run-parent", "fanout")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-parent", NodeID: "gate", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateApproval(ctx, store.Approval{
		RunID: "run-parent", NodeID: "gate", RequestedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-child", Pipeline: "child", TriggerSource: "cli",
		ParentRunID: "run-parent", ParentNodeID: "fanout",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/runs/run-parent?include=nodes")
	if err != nil {
		t.Fatalf("GET run detail: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var body struct {
		Nodes []struct {
			ID          string `json:"id"`
			Decorations *struct {
				ApprovalState    *json.RawMessage `json:"approval_state"`
				SpawnedPipelines []struct {
					Pipeline   string `json:"pipeline"`
					ChildRunID string `json:"child_run_id"`
				} `json:"spawned_pipelines"`
			} `json:"decorations"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Nodes) != 2 {
		t.Fatalf("nodes=%d want 2", len(body.Nodes))
	}
	byID := map[string]int{}
	for i, n := range body.Nodes {
		byID[n.ID] = i
	}
	fanout := body.Nodes[byID["fanout"]]
	if fanout.Decorations == nil || len(fanout.Decorations.SpawnedPipelines) != 1 {
		t.Fatalf("fanout decorations=%+v want one spawned pipeline", fanout.Decorations)
	}
	if got := fanout.Decorations.SpawnedPipelines[0].ChildRunID; got != "run-child" {
		t.Errorf("child run id=%q want run-child", got)
	}
	gate := body.Nodes[byID["gate"]]
	if gate.Decorations == nil || gate.Decorations.ApprovalState == nil {
		t.Fatalf("gate decorations=%+v want an approval state", gate.Decorations)
	}
}
