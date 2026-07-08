package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Mixed-Dep wiring exercises every Plan-layer implementor in one
// Needs call. A regression where one of the types stops satisfying
// [sparkwing.Dep] surfaces as a compile error, not a runtime failure.
func TestDep_MixedTypesCompose(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", &buildJob{})
	gate := sparkwing.JobApproval(plan, "review", sparkwing.ApprovalConfig{
		OnExpiry: sparkwing.ApprovalDeny,
	})
	group := sparkwing.GroupJobs(plan, "shards", a)
	leaf := sparkwing.Job(plan, "leaf", &buildJob{}).
		Needs(a, gate, group)

	got := map[string]struct{}{}
	for _, d := range leaf.DepIDs() {
		got[d] = struct{}{}
	}
	for _, want := range []string{"a", "review"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing dep %q in %v", want, leaf.DepIDs())
		}
	}
}

// WorkDep parallels Dep at the Work layer: every Work-layer dep type
// must compose in a single Needs call. Compile-time conformance plus
// a runtime smoke that the deps land on the step.
func TestWorkDep_MixedTypesCompose(t *testing.T) {
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { return nil })
	grp := sparkwing.GroupSteps(w, "shards", a, b)
	leaf := sparkwing.Step(w, "leaf", func(ctx context.Context) error { return nil }).
		Needs(a, grp)

	got := map[string]struct{}{}
	for _, d := range leaf.DepIDs() {
		got[d] = struct{}{}
	}
	for _, want := range []string{"a", "b"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing dep %q in %v", want, leaf.DepIDs())
		}
	}
}
