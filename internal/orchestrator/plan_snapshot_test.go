package orchestrator

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

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

// A node that declares both Cache and Concurrency must carry the two
// concerns independently in the snapshot: the content-cache fact (with
// TTL) and the concurrency facts (group, capacity, cost, scope,
// policy, timeouts). No Coalesce, no shared "cache_*" field.
func TestMarshalPlanSnapshot_SplitsCacheAndConcurrency(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
		Capacity:      8,
		Scope:         sparkwing.ScopeBox,
		OnLimit:       sparkwing.Queue,
		QueueTimeout:  30 * time.Second,
		CancelTimeout: 10 * time.Second,
	})
	sparkwing.Job(plan, "shard", func(ctx context.Context) error { return nil }).
		Concurrency(g, 4).
		Cache(func(ctx context.Context) sparkwing.CacheKey { return sparkwing.Key("coverage", "shard") },
			sparkwing.TTL(48*time.Hour))

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
	if err != nil {
		t.Fatalf("marshalPlanSnapshot: %v", err)
	}
	var snap planSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].Modifiers == nil {
		t.Fatalf("expected one node with modifiers, got %+v", snap.Nodes)
	}
	m := snap.Nodes[0].Modifiers

	if !m.Cache {
		t.Errorf("Cache flag not set")
	}
	if m.CacheTTLMS != (48 * time.Hour).Milliseconds() {
		t.Errorf("CacheTTLMS = %d, want %d", m.CacheTTLMS, (48 * time.Hour).Milliseconds())
	}

	if m.ConcGroup != "db" {
		t.Errorf("ConcGroup = %q, want db", m.ConcGroup)
	}
	if m.ConcCapacity != 8 {
		t.Errorf("ConcCapacity = %d, want 8", m.ConcCapacity)
	}
	if m.ConcCost != 4 {
		t.Errorf("ConcCost = %d, want 4", m.ConcCost)
	}
	if m.ConcScope != string(sparkwing.ScopeBox) {
		t.Errorf("ConcScope = %q, want %q", m.ConcScope, sparkwing.ScopeBox)
	}
	if m.ConcQueueTimeoutMS != (30 * time.Second).Milliseconds() {
		t.Errorf("ConcQueueTimeoutMS = %d, want %d", m.ConcQueueTimeoutMS, (30 * time.Second).Milliseconds())
	}
	if m.ConcCancelTimeoutMS != (10 * time.Second).Milliseconds() {
		t.Errorf("ConcCancelTimeoutMS = %d, want %d", m.ConcCancelTimeoutMS, (10 * time.Second).Milliseconds())
	}

	for _, banned := range []string{"cache_max", "cache_on_limit", "coalesce"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("snapshot JSON still contains %q", banned)
		}
	}
}

func TestMarshalPlanSnapshot_EmbedsWorkAndSpawnTargets(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "parent", snapshotParentJob{}).Retry(2)

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
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
type (
	snapshotCycleA struct{}
	snapshotCycleB struct{}
)

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

	_, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
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

// snapshotGroupSteps declares two GroupSteps inside its Work so the
// snapshot walker has groups to serialize.
type snapshotGroupSteps struct{}

func (snapshotGroupSteps) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetch := sparkwing.Step(w, "fetch", func(ctx context.Context) error { return nil })
	lint := sparkwing.Step(w, "lint", func(ctx context.Context) error { return nil }).Needs(fetch)
	vet := sparkwing.Step(w, "vet", func(ctx context.Context) error { return nil }).Needs(fetch)
	test := sparkwing.Step(w, "test", func(ctx context.Context) error { return nil }).Needs(fetch)
	smoke := sparkwing.Step(w, "smoke", func(ctx context.Context) error { return nil })
	bench := sparkwing.Step(w, "bench", func(ctx context.Context) error { return nil })
	sparkwing.GroupSteps(w, "ci", lint, vet, test)
	sparkwing.GroupSteps(w, "verify", smoke, bench)
	return nil, nil
}

func TestMarshalPlanSnapshot_EmitsStepGroupsInDeclarationOrder(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "grouped", snapshotGroupSteps{})

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
	if err != nil {
		t.Fatalf("marshalPlanSnapshot: %v", err)
	}
	var snap planSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].Work == nil {
		t.Fatalf("missing node work: %+v", snap.Nodes)
	}
	groups := snap.Nodes[0].Work.StepGroups
	if len(groups) != 2 {
		t.Fatalf("step_groups len=%d, want 2", len(groups))
	}
	if groups[0].Name != "ci" {
		t.Errorf("step_groups[0].Name = %q, want %q", groups[0].Name, "ci")
	}
	wantCI := []string{"lint", "vet", "test"}
	if !reflect.DeepEqual(groups[0].Members, wantCI) {
		t.Errorf("step_groups[0].Members = %v, want %v", groups[0].Members, wantCI)
	}
	if groups[1].Name != "verify" {
		t.Errorf("step_groups[1].Name = %q, want %q", groups[1].Name, "verify")
	}
	wantVerify := []string{"smoke", "bench"}
	if !reflect.DeepEqual(groups[1].Members, wantVerify) {
		t.Errorf("step_groups[1].Members = %v, want %v", groups[1].Members, wantVerify)
	}

	again, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var roundTrip planSnapshot
	if err := json.Unmarshal(again, &roundTrip); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	rtGroups := roundTrip.Nodes[0].Work.StepGroups
	if !reflect.DeepEqual(groups, rtGroups) {
		t.Errorf("round-trip diverged:\n  before=%+v\n  after=%+v", groups, rtGroups)
	}
}

func TestMarshalPlanSnapshot_RendersSpawnEachTemplate(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "each", snapshotForEachJob{})

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
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

func TestMarshalPlanSnapshot_CarriesResourceHints(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Resources(sparkwing.Cores(4), sparkwing.MemoryGB(8))
	sparkwing.Job(plan, "build", func(ctx context.Context) error { return nil }).
		Resources(sparkwing.Cores(2), sparkwing.MemoryGB(1))
	sparkwing.Job(plan, "plain", func(ctx context.Context) error { return nil })

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
	if err != nil {
		t.Fatalf("marshalPlanSnapshot: %v", err)
	}
	var snap planSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if snap.Resources == nil {
		t.Fatal("plan_resources missing from snapshot")
	}
	if snap.Resources.Cores != 4 {
		t.Errorf("plan Cores = %v, want 4", snap.Resources.Cores)
	}
	if want := int64(8 * (1 << 30)); snap.Resources.MemoryBytes != want {
		t.Errorf("plan MemoryBytes = %d, want %d", snap.Resources.MemoryBytes, want)
	}

	byID := map[string]snapshotNode{}
	for _, n := range snap.Nodes {
		byID[n.ID] = n
	}
	m := byID["build"].Modifiers
	if m == nil {
		t.Fatal("build node lost its modifiers")
	}
	if m.ResCores != 2 {
		t.Errorf("node ResCores = %v, want 2", m.ResCores)
	}
	if want := int64(1 << 30); m.ResMemoryBytes != want {
		t.Errorf("node ResMemoryBytes = %d, want %d", m.ResMemoryBytes, want)
	}
	if byID["plain"].Modifiers != nil {
		t.Errorf("plain node grew modifiers: %+v", byID["plain"].Modifiers)
	}
}

func TestMarshalPlanSnapshot_CarriesPriority(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Priority(100)
	sparkwing.Job(plan, "build", func(context.Context) error { return nil })

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
	if err != nil {
		t.Fatalf("marshalPlanSnapshot: %v", err)
	}
	if got := planPriorityFromSnapshot(raw); got != 100 {
		t.Fatalf("priority = %d, want 100", got)
	}
}

func TestMarshalPlanSnapshot_OmitsResourcesWhenUndeclared(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "build", func(ctx context.Context) error { return nil })
	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "explain"}, planSnapshotMeta{})
	if err != nil {
		t.Fatalf("marshalPlanSnapshot: %v", err)
	}
	if strings.Contains(string(raw), "plan_resources") {
		t.Errorf("snapshot emitted plan_resources for an undeclared plan: %s", raw)
	}
}
