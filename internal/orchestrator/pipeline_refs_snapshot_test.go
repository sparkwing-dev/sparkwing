package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type refHoldingJob struct {
	sparkwing.Base
	Last  sparkwing.Ref[struct{}]
	InRun sparkwing.Ref[struct{}]
}

func (refHoldingJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(context.Context) error { return nil })
	return nil, nil
}

// A node declares in the plan snapshot the other pipelines' nodes its job
// holds a RefToLastRun field for, which is all a claimed node may read of
// another pipeline; an in-run ref and a node holding none declare nothing.
func TestPlanSnapshotDeclaresTheLastRunRefsAJobHolds(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "deploy", &refHoldingJob{
		Last:  sparkwing.RefToLastRun[struct{}]("build", "artifact"),
		InRun: sparkwing.Ref[struct{}]{NodeID: "sibling"},
	})
	sparkwing.Job(plan, "sibling", &refHoldingJob{})
	snap, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "release", RunID: "run-1"}, planSnapshotMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(snap), `"pipeline_refs"`); got != 1 ||
		!strings.Contains(string(snap), `"pipeline_refs":[{"pipeline":"build","node":"artifact"}]`) {
		t.Fatalf("snapshot %s declares %d pipeline_refs, want only deploy's build/artifact", snap, got)
	}
}
