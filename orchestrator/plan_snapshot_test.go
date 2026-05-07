package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// snapshotChildJob is a Spawn target with no inner spawns. Used to
// verify SpawnNode targets recurse into snapshotWork.
type snapshotChildJob struct{}

func (snapshotChildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "scan", func(ctx context.Context) error { return nil })
	return nil, nil
}

// snapshotParentJob spawns a snapshotChildJob from inside its Work so
// the snapshot walker has to recurse one level.
type snapshotParentJob struct{}

func (snapshotParentJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	analyze := sparkwing.Step(w, "analyze", func(ctx context.Context) error { return nil })
	sparkwing.JobSpawn(w, "scan-child", snapshotChildJob{}).Needs(analyze)
	return nil, nil
}

func TestMarshalPlanSnapshot_EmbedsWorkAndSpawnTargets(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "parent", snapshotParentJob{}).Retry(2)

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"})
	if err != nil {
		t.Fatalf("marshalPlanSnapshot: %v", err)
	}
	var snap planSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := len(snap.Nodes), 1; got != want {
		t.Fatalf("nodes=%d, want %d", got, want)
	}
	n := snap.Nodes[0]
	if n.Modifiers == nil || n.Modifiers.Retry != 2 {
		t.Errorf("Retry modifier missing: %+v", n.Modifiers)
	}
	if n.Work == nil {
		t.Fatalf("Work missing")
	}
	if got, want := len(n.Work.Steps), 1; got != want {
		t.Errorf("steps=%d, want %d", got, want)
	}
	if got, want := len(n.Work.Spawns), 1; got != want {
		t.Fatalf("spawns=%d, want %d", got, want)
	}
	sp := n.Work.Spawns[0]
	if sp.ID != "scan-child" {
		t.Errorf("spawn id=%q", sp.ID)
	}
	if sp.TargetWork == nil {
		t.Fatalf("spawn TargetWork missing")
	}
	if got, want := len(sp.TargetWork.Steps), 1; got != want {
		t.Errorf("target steps=%d, want %d", got, want)
	}
	if sp.TargetWork.Steps[0].ID != "scan" {
		t.Errorf("target step=%q", sp.TargetWork.Steps[0].ID)
	}
}

// snapshotCycleA spawns snapshotCycleB; snapshotCycleB spawns
// snapshotCycleA. The snapshot walker must catch this without
// recursing forever.
type snapshotCycleA struct{}
type snapshotCycleB struct{}

func (snapshotCycleA) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawn(w, "to-b", snapshotCycleB{})
	return nil, nil
}

func (snapshotCycleB) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawn(w, "to-a", snapshotCycleA{})
	return nil, nil
}

func TestMarshalPlanSnapshot_DetectsSpawnCycle(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "root", snapshotCycleA{})

	_, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"})
	if err == nil {
		t.Fatalf("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "spawn cycle detected") {
		t.Errorf("error %q lacks 'spawn cycle detected'", err.Error())
	}
}

// snapshotForEachJob declares a SpawnNodeForEach so the walker exercises
// the zero-value-template materialization path.
type snapshotForEachJob struct{}

func (snapshotForEachJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	items := []string{"a", "b", "c"}
	sparkwing.JobSpawnEach(w, items, func(item string) (string, sparkwing.Workable) {
		return "shard-" + item, snapshotChildJob{}
	})
	return nil, nil
}

func TestMarshalPlanSnapshot_RendersSpawnEachTemplate(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "each", snapshotForEachJob{})

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"})
	if err != nil {
		t.Fatalf("marshalPlanSnapshot: %v", err)
	}
	var snap planSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.Nodes) != 1 {
		t.Fatalf("nodes=%d, want 1", len(snap.Nodes))
	}
	w := snap.Nodes[0].Work
	if w == nil || len(w.SpawnEach) != 1 {
		t.Fatalf("spawn_each not rendered: %+v", w)
	}
	se := w.SpawnEach[0]
	if se.ItemTemplateWork == nil || len(se.ItemTemplateWork.Steps) != 1 {
		t.Errorf("template work not materialized: %+v", se)
	}
	if !strings.HasPrefix(se.ID, "__spawn_each_") {
		t.Errorf("synthetic id missing: %q", se.ID)
	}
}
