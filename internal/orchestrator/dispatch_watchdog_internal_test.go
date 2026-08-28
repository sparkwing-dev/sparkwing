package orchestrator

import (
	"context"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestStuckNodeIDsIncludesScheduledNodes(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "static", func(ctx context.Context) error { return nil })
	resolved := sparkwing.Job(plan, "resolved", func(ctx context.Context) error { return nil })

	aux := sparkwing.NewPlan()
	dynamic := sparkwing.Job(aux, "dynamic", func(ctx context.Context) error { return nil })

	state := &dispatchState{
		outcomes:  map[string]sparkwing.Outcome{resolved.ID(): sparkwing.Success},
		scheduled: map[string]*sparkwing.JobNode{dynamic.ID(): dynamic},
	}

	got := stuckNodeIDs(plan, state)
	if !slices.Contains(got, "static") {
		t.Fatalf("stuckNodeIDs = %v, want static plan node", got)
	}
	if !slices.Contains(got, dynamic.ID()) {
		t.Fatalf("stuckNodeIDs = %v, want runtime-scheduled dynamic node %q", got, dynamic.ID())
	}
	if slices.Contains(got, resolved.ID()) {
		t.Fatalf("stuckNodeIDs = %v, resolved node %q must not be reported", got, resolved.ID())
	}
}
