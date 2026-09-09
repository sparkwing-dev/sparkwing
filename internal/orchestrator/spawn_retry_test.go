package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type spawnResult struct{}

type spawnerNode struct{ sparkwing.Base }

func (job *spawnerNode) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run)
	return nil, nil
}

func (spawnerNode) run(ctx context.Context) error {
	_, err := sparkwing.RunAndAwait[spawnResult, sparkwing.NoInputs](ctx, "spawn-retry-child", "out",
		sparkwing.WithFreshTimeout(150*time.Millisecond))
	return err
}

type spawnRetryParentPipe struct{ sparkwing.Base }

func (spawnRetryParentPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "spawner", &spawnerNode{})
	return nil
}

var gateCounter struct {
	mu       sync.Mutex
	attempts int
}

func resetGateCounter() {
	gateCounter.mu.Lock()
	defer gateCounter.mu.Unlock()
	gateCounter.attempts = 0
}

type earlyGate struct{ sparkwing.Base }

func (gate *earlyGate) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", gate.run)
	return nil, nil
}

func (earlyGate) run(ctx context.Context) error {
	gateCounter.mu.Lock()
	attempt := gateCounter.attempts
	gateCounter.attempts++
	gateCounter.mu.Unlock()
	if attempt == 0 {
		return errors.New("first-attempt gate failure")
	}
	return nil
}

type earlyFailSpawnPipe struct{ sparkwing.Base }

func (earlyFailSpawnPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	gate := sparkwing.Job(plan, "gate", &earlyGate{})
	sparkwing.Job(plan, "spawner", &spawnerNode{}).Needs(gate)
	return nil
}

type spawnRetryChildPipe struct{ sparkwing.Base }

func (spawnRetryChildPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, runContext.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

func init() {
	register("spawn-retry-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &spawnRetryParentPipe{} })
	register("spawn-retry-early-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &earlyFailSpawnPipe{} })
	register("spawn-retry-child", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &spawnRetryChildPipe{} })
}

func TestRun_NestedSpawnRetryOf_Chained(t *testing.T) {
	paths := newPaths(t)
	ctx := context.Background()
	runStore, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = runStore.Close() }()

	first, err := orchestrator.Run(ctx, manualChildDispatchBackends(paths, runStore),
		orchestrator.Options{Pipeline: "spawn-retry-parent", RunID: "p1"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != "failed" {
		t.Fatalf("first run status = %q, want failed after child timeout", first.Status)
	}

	firstChildID, err := runStore.FindSpawnedChildTriggerID(ctx, "p1", "spawner", "spawn-retry-child")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID(p1): %v", err)
	}
	if firstChildID == "" {
		t.Fatal("expected first run's spawner to record a child trigger row")
	}

	second, err := orchestrator.Run(ctx, manualChildDispatchBackends(paths, runStore),
		orchestrator.Options{Pipeline: "spawn-retry-parent", RunID: "p2", RetryOf: "p1"})
	if err != nil {
		t.Fatalf("second (retry) run: %v", err)
	}
	if second.Status != "failed" {
		t.Fatalf("second run status = %q, want failed while child remains unclaimed", second.Status)
	}

	secondChildID, err := runStore.FindSpawnedChildTriggerID(ctx, "p2", "spawner", "spawn-retry-child")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID(p2): %v", err)
	}
	if secondChildID == "" {
		t.Fatal("expected retry's spawner to record a new child trigger row")
	}
	if secondChildID == firstChildID {
		t.Fatalf("retry's child trigger id %q matches first run's; should be a fresh row", secondChildID)
	}

	trigger, err := runStore.GetTrigger(ctx, secondChildID)
	if err != nil {
		t.Fatalf("GetTrigger(secondChild): %v", err)
	}
	if trigger.RetryOf != firstChildID {
		t.Fatalf("second child retry_of = %q, want %q",
			trigger.RetryOf, firstChildID)
	}
	if trigger.ParentRunID != "p2" {
		t.Fatalf("second child parent_run_id = %q, want %q", trigger.ParentRunID, "p2")
	}
	if trigger.ParentNodeID != "spawner" {
		t.Fatalf("second child parent_node_id = %q, want %q", trigger.ParentNodeID, "spawner")
	}
}

func TestRun_NestedSpawnRetryOf_NoPriorChild(t *testing.T) {
	resetGateCounter()
	paths := newPaths(t)
	ctx := context.Background()
	runStore, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = runStore.Close() }()

	first, err := orchestrator.Run(ctx, manualChildDispatchBackends(paths, runStore),
		orchestrator.Options{Pipeline: "spawn-retry-early-fail", RunID: "ef1"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != "failed" {
		t.Fatalf("first run status = %q, want failed after gate rejection", first.Status)
	}

	priorChildID, err := runStore.FindSpawnedChildTriggerID(ctx, "ef1", "spawner", "spawn-retry-child")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID(ef1): %v", err)
	}
	if priorChildID != "" {
		t.Fatalf("first run created child trigger %q at spawner; expected none", priorChildID)
	}

	_, err = orchestrator.Run(ctx, manualChildDispatchBackends(paths, runStore),
		orchestrator.Options{Pipeline: "spawn-retry-early-fail", RunID: "ef2", RetryOf: "ef1"})
	if err != nil {
		t.Fatalf("second (retry) run: %v", err)
	}

	retryChildID, err := runStore.FindSpawnedChildTriggerID(ctx, "ef2", "spawner", "spawn-retry-child")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID(ef2): %v", err)
	}
	if retryChildID == "" {
		t.Fatal("expected retry's spawner to record a child trigger row")
	}
	trigger, err := runStore.GetTrigger(ctx, retryChildID)
	if err != nil {
		t.Fatalf("GetTrigger(retryChild): %v", err)
	}
	if trigger.RetryOf != "" {
		t.Fatalf("retry's child retry_of = %q, want empty (no prior child to chain from)", trigger.RetryOf)
	}
	if trigger.ParentRunID != "ef2" {
		t.Fatalf("retry's child parent_run_id = %q, want %q", trigger.ParentRunID, "ef2")
	}
	if trigger.ParentNodeID != "spawner" {
		t.Fatalf("retry's child parent_node_id = %q, want %q", trigger.ParentNodeID, "spawner")
	}
}
