package orchestrator

import (
	"strings"
	"testing"
	"time"

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
