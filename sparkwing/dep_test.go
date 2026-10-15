package sparkwing

import (
	"context"
	"slices"
	"testing"
)

func TestJobDependenciesAcceptApprovalAndGroup(t *testing.T) {
	plan := NewPlan()
	build := Job(plan, "build", func(context.Context) error { return nil })
	approval := plan.Approval("review", ApprovalConfig{OnTimeout: ApprovalDeny})
	group := GroupJobs(plan, "shards", build)
	leaf := Job(plan, "leaf", func(context.Context) error { return nil }).Needs(build, approval, group)
	if !slices.Equal(leaf.needs, []string{"build", "review"}) {
		t.Fatalf("job prerequisites = %v", leaf.needs)
	}
}

func TestStepDependenciesAcceptGroup(t *testing.T) {
	work := NewWork()
	a := Step(work, "a", func(context.Context) error { return nil })
	b := Step(work, "b", func(context.Context) error { return nil })
	group := GroupSteps(work, "shards", a, b)
	leaf := Step(work, "leaf", func(context.Context) error { return nil }).Needs(a, group)
	if !slices.Equal(leaf.needs, []string{"a", "b"}) {
		t.Fatalf("step prerequisites = %v", leaf.needs)
	}
}
