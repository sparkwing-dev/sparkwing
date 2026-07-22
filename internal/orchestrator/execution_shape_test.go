package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type executionShapeJob struct {
	sparkwing.Base
	Variant string
}

func (*executionShapeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(context.Context) error { return nil })
	return nil, nil
}

type alternateExecutionShapeJob struct{ sparkwing.Base }

func (*alternateExecutionShapeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(context.Context) error { return nil })
	return nil, nil
}

func TestExecutionShapeHash_IsStableAcrossDependencyDeclarationOrder(t *testing.T) {
	leftPlan := sparkwing.NewPlan()
	leftA := sparkwing.Job(leftPlan, "a", &executionShapeJob{})
	leftB := sparkwing.Job(leftPlan, "b", &executionShapeJob{})
	left := sparkwing.Job(leftPlan, "target", &executionShapeJob{}).Needs(leftA, leftB)

	rightPlan := sparkwing.NewPlan()
	rightA := sparkwing.Job(rightPlan, "a", &executionShapeJob{})
	rightB := sparkwing.Job(rightPlan, "b", &executionShapeJob{})
	right := sparkwing.Job(rightPlan, "target", &executionShapeJob{}).Needs(rightB, rightA)

	if got, want := executionShapeHash(left), executionShapeHash(right); got != want {
		t.Fatalf("dependency declaration order changed shape: %q != %q", got, want)
	}
}

func TestExecutionShapeHash_Golden(t *testing.T) {
	plan := sparkwing.NewPlan()
	dep := sparkwing.Job(plan, "source", &executionShapeJob{})
	node := sparkwing.Job(plan, "target", &executionShapeJob{}).
		Needs(dep).
		Resources(sparkwing.Cores(2), sparkwing.MemoryGB(1)).
		Retry(2, sparkwing.RetryBackoff(time.Second)).
		Timeout(time.Minute).
		Requires("linux")
	const want = "a37ac7f01d061c33f6d3045fe3eac27f954e8baec1c6ed411f2b961d22b74e69"
	if got := executionShapeHash(node); got != want {
		t.Fatalf("execution shape = %q, want %q", got, want)
	}
}

func TestExecutionShapeHash_ChangesWithCostBearingExecutionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sparkwing.JobNode)
	}{
		{name: "resources", mutate: func(n *sparkwing.JobNode) { n.Resources(sparkwing.Cores(2)) }},
		{name: "retry", mutate: func(n *sparkwing.JobNode) { n.Retry(2, sparkwing.RetryBackoff(time.Second)) }},
		{name: "timeout", mutate: func(n *sparkwing.JobNode) { n.Timeout(time.Minute) }},
		{name: "cache", mutate: func(n *sparkwing.JobNode) { n.Cache(func(context.Context) sparkwing.CacheKey { return "v1" }) }},
		{name: "runner", mutate: func(n *sparkwing.JobNode) { n.Requires("linux") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			basePlan := sparkwing.NewPlan()
			base := sparkwing.Job(basePlan, "target", &executionShapeJob{})
			changedPlan := sparkwing.NewPlan()
			changed := sparkwing.Job(changedPlan, "target", &executionShapeJob{})
			tt.mutate(changed)
			if got, wantNot := executionShapeHash(changed), executionShapeHash(base); got == wantNot {
				t.Fatalf("cost-bearing %s mutation left shape unchanged: %q", tt.name, got)
			}
		})
	}
}

func TestExecutionShapeHash_ChangesWithActionType(t *testing.T) {
	leftPlan := sparkwing.NewPlan()
	left := sparkwing.Job(leftPlan, "target", &executionShapeJob{})
	rightPlan := sparkwing.NewPlan()
	right := sparkwing.Job(rightPlan, "target", &alternateExecutionShapeJob{})

	if got, wantNot := executionShapeHash(right), executionShapeHash(left); got == wantNot {
		t.Fatalf("action type left shape unchanged: %q", got)
	}
}

func TestExecutionShapeHash_ChangesWithActionConfiguration(t *testing.T) {
	leftPlan := sparkwing.NewPlan()
	left := sparkwing.Job(leftPlan, "target", &executionShapeJob{Variant: "small"})
	rightPlan := sparkwing.NewPlan()
	right := sparkwing.Job(rightPlan, "target", &executionShapeJob{Variant: "large"})

	if got, wantNot := executionShapeHash(right), executionShapeHash(left); got == wantNot {
		t.Fatalf("action configuration left shape unchanged: %q", got)
	}
}
