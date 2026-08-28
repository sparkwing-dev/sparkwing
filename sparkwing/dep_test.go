package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
