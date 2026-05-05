package api_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// fixtureSnapshot exercises every decoration lists as an
// acceptance criterion: a node with modifiers, an approval gate, a
// group membership, an OnFailure relationship, and an inner-Work
// DAG (steps + spawn). Plus a bare node with no decorations to
// confirm we don't synthesize an empty entry for it.
const fixtureSnapshot = `{
  "nodes": [
    {
      "id": "build",
      "groups": ["ci"],
      "modifiers": {
        "retry": 3,
        "timeout_ms": 60000,
        "runs_on": ["linux"]
      },
      "work": {
        "steps": [
          {"id": "compile"},
          {"id": "package", "needs": ["compile"], "is_result": true}
        ],
        "spawns": [
          {"id": "fanout", "needs": ["package"], "target_job": "deploy"}
        ],
        "result_step": "package"
      }
    },
    {
      "id": "release",
      "approval": {"message": "ship it?"}
    },
    {
      "id": "rollback",
      "on_failure_of": "release"
    },
    {
      "id": "expand",
      "dynamic": true
    },
    {
      "id": "plain"
    }
  ]
}`

func TestDecorationsFromSnapshot(t *testing.T) {
	got := api.DecorationsFromSnapshot([]byte(fixtureSnapshot))
	if got == nil {
		t.Fatal("DecorationsFromSnapshot returned nil for non-empty snapshot")
	}
	if _, ok := got["plain"]; ok {
		t.Errorf("plain node should have no decorations entry; got %+v", got["plain"])
	}

	build, ok := got["build"]
	if !ok {
		t.Fatal("missing decorations for build")
	}
	if len(build.Groups) != 1 || build.Groups[0] != "ci" {
		t.Errorf("build.groups=%v want [ci]", build.Groups)
	}
	if build.Modifiers == nil || build.Modifiers.Retry != 3 || build.Modifiers.TimeoutMS != 60000 {
		t.Errorf("build.modifiers=%+v want retry=3 timeout_ms=60000", build.Modifiers)
	}
	if len(build.Modifiers.RunsOn) != 1 || build.Modifiers.RunsOn[0] != "linux" {
		t.Errorf("build.modifiers.runs_on=%v want [linux]", build.Modifiers.RunsOn)
	}
	if build.Work == nil || len(build.Work.Steps) != 2 || build.Work.ResultStep != "package" {
		t.Errorf("build.work=%+v missing inner-work tree", build.Work)
	}
	if len(build.Work.Spawns) != 1 || build.Work.Spawns[0].TargetJob != "deploy" {
		t.Errorf("build.work.spawns=%+v want one targeting deploy", build.Work.Spawns)
	}

	release, ok := got["release"]
	if !ok || !release.Approval {
		t.Errorf("release.approval=%v want true", release)
	}

	rollback, ok := got["rollback"]
	if !ok || rollback.OnFailureOf != "release" {
		t.Errorf("rollback.on_failure_of=%q want release", rollback.OnFailureOf)
	}

	expand, ok := got["expand"]
	if !ok || !expand.Dynamic {
		t.Errorf("expand.dynamic=%v want true", expand)
	}
}

// TestDecorationsFromSnapshot_EmptyOrUnparseable pins the
// graceful-degradation contract: empty input -> nil; malformed JSON
// -> nil. Callers treat both as "no decorations" and the dashboard
// falls back to rendering nodes without adornments.
func TestDecorationsFromSnapshot_EmptyOrUnparseable(t *testing.T) {
	if got := api.DecorationsFromSnapshot(nil); got != nil {
		t.Errorf("nil snapshot: got %v want nil", got)
	}
	if got := api.DecorationsFromSnapshot([]byte{}); got != nil {
		t.Errorf("empty snapshot: got %v want nil", got)
	}
	if got := api.DecorationsFromSnapshot([]byte("not json")); got != nil {
		t.Errorf("garbage snapshot: got %v want nil", got)
	}
}

// TestDecorateNodes_WireShape pins the on-the-wire JSON shape: each
// node marshals as the flat store.Node fields plus an optional
// `decorations` object. Nodes without snapshot-derived adornments
// emit no decorations key (omitempty).
func TestDecorateNodes_WireShape(t *testing.T) {
	nodes := []*store.Node{
		{NodeID: "build", Status: "running", Deps: []string{}},
		{NodeID: "plain", Status: "pending", Deps: []string{}},
	}
	wrapped := api.DecorateNodes(nodes, []byte(fixtureSnapshot))
	if len(wrapped) != 2 {
		t.Fatalf("len=%d want 2", len(wrapped))
	}
	body, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip []map[string]any
	if err := json.Unmarshal(body, &roundtrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// build node carries decorations.modifiers and decorations.work.
	if roundtrip[0]["id"] != "build" {
		t.Errorf("first node id=%v want build", roundtrip[0]["id"])
	}
	deco, ok := roundtrip[0]["decorations"].(map[string]any)
	if !ok {
		t.Fatalf("build node missing decorations: %v", roundtrip[0])
	}
	if _, ok := deco["modifiers"].(map[string]any); !ok {
		t.Errorf("build.decorations.modifiers missing: %v", deco)
	}
	if _, ok := deco["work"].(map[string]any); !ok {
		t.Errorf("build.decorations.work missing: %v", deco)
	}
	// plain node: store.Node fields only, no decorations key.
	if roundtrip[1]["id"] != "plain" {
		t.Errorf("second node id=%v want plain", roundtrip[1]["id"])
	}
	if _, ok := roundtrip[1]["decorations"]; ok {
		t.Errorf("plain node leaked a decorations key: %v", roundtrip[1])
	}
}

// TestDecorateNodes_NoSnapshot pins the additive-shape contract:
// when the run has no PlanSnapshot, every wrapped node still
// marshals as bare store.Node fields with no `decorations` key.
// Existing CLI consumers that read the canonical store fields are
// unaffected.
func TestDecorateNodes_NoSnapshot(t *testing.T) {
	nodes := []*store.Node{{NodeID: "a", Status: "pending", Deps: []string{}}}
	wrapped := api.DecorateNodes(nodes, nil)
	body, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip []map[string]any
	if err := json.Unmarshal(body, &roundtrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := roundtrip[0]["decorations"]; ok {
		t.Errorf("nil snapshot leaked decorations key: %v", roundtrip[0])
	}
	if roundtrip[0]["id"] != "a" {
		t.Errorf("id=%v want a", roundtrip[0]["id"])
	}
}
