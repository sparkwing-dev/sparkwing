package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The controller reads a pipeline's claim wait off the plan snapshot, on the
// planner path and the orchestrator path alike.
func TestPlanSnapshotCarriesTheClaimWait(t *testing.T) {
	plan := sparkwing.NewPlan().ClaimWait(90 * time.Minute)
	snap, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "deploy", RunID: "run-1"}, planSnapshotMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snap), `"claim_wait_ms":5400000`) {
		t.Fatalf("snapshot %s lacks the claim wait", snap)
	}
}

// The engine host enforces plan-level groups from the snapshot alone, so it
// carries each group's whole limit, not only its key.
func TestPlanSnapshotCarriesPlanConcurrencyLimits(t *testing.T) {
	plan := sparkwing.NewPlan().Concurrency(sparkwing.NewConcurrencyGroup("deploys", sparkwing.ConcurrencyLimit{
		Capacity: 3, Scope: sparkwing.ScopeRun, OnLimit: sparkwing.Queue, QueueTimeout: time.Minute, CancelTimeout: 2 * time.Second,
	}), 2)
	snap, err := buildPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "deploy", RunID: "run-1"}, planSnapshotMeta{})
	if err != nil {
		t.Fatal(err)
	}
	want := nodeproto.PlanConc{
		Key: nodeproto.ConcurrencyGroupKey("deploys", "run", "run-1", nil), Capacity: 3, Cost: 2,
		OnLimit: "queue", QueueTimeoutMS: 60000, CancelTimeoutMS: 2000,
	}
	if len(snap.PlanConcs) != 1 || snap.PlanConcs[0] != want || snap.PlanConc == nil || *snap.PlanConc != want {
		t.Fatalf("plan concurrency %+v / %+v, want %+v", snap.PlanConc, snap.PlanConcs, want)
	}
}
