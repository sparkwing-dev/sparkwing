package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestChildRun_StartAndFinishEventsInParentStream: a node that spawns a
// child via RunAndAwait must record structured child_run_start /
// child_run_finish events in the PARENT's stream -- linking the child's
// run_id and terminal status -- rather than inlining the child's
// per-line output. Reuses spawn-retry-parent, whose spawner awaits with
// a tight timeout so the child_run_finish lands as status=timeout
// without needing a worker to process the trigger.
func TestChildRun_StartAndFinishEventsInParentStream(t *testing.T) {
	p := newPaths(t)
	ctx := context.Background()

	res, err := orchestrator.RunLocal(ctx, p,
		orchestrator.Options{Pipeline: "spawn-retry-parent", RunID: "cr1"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed (spawn await times out)", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	events, err := st.ListEventsAfter(ctx, "cr1", 0, 1000)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	var startPayload, finishPayload []byte
	for _, e := range events {
		switch e.Kind {
		case "child_run_start":
			startPayload = e.Payload
		case "child_run_finish":
			finishPayload = e.Payload
		case "exec_line":
			t.Errorf("parent stream contains exec_line; child output must not be inlined")
		case "pipeline_await_spawned":
			t.Errorf("legacy pipeline_await_spawned event still emitted; want child_run_start")
		}
	}

	if startPayload == nil {
		t.Fatal("missing child_run_start event in parent stream")
	}
	if finishPayload == nil {
		t.Fatal("missing child_run_finish event in parent stream")
	}

	var start, finish map[string]any
	if err := json.Unmarshal(startPayload, &start); err != nil {
		t.Fatalf("unmarshal child_run_start: %v", err)
	}
	if err := json.Unmarshal(finishPayload, &finish); err != nil {
		t.Fatalf("unmarshal child_run_finish: %v", err)
	}

	if start["pipeline"] != "spawn-retry-child" {
		t.Errorf("child_run_start pipeline = %v, want spawn-retry-child", start["pipeline"])
	}
	childID, _ := start["child_run_id"].(string)
	if childID == "" {
		t.Error("child_run_start missing child_run_id")
	}
	if finish["status"] != "timeout" {
		t.Errorf("child_run_finish status = %v, want timeout", finish["status"])
	}
	if _, ok := finish["duration_ms"]; !ok {
		t.Error("child_run_finish missing duration_ms")
	}
	if finish["child_run_id"] != childID {
		t.Errorf("child_run_finish child_run_id = %v, want %v (must match start)", finish["child_run_id"], childID)
	}
}
