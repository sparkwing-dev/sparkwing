package sparkwing

import (
	"context"
	"slices"
	"testing"
)

func TestRunnerLabelsReachPlanDocument(t *testing.T) {
	plan := NewPlan()
	Job(plan, "build", func(context.Context) error { return nil }).
		Requires("gpu", "arch=arm64").Prefers("cloud-linux")
	job := planDocument(plan).Jobs[0]
	if !slices.Equal(job.Requires, []string{"gpu", "arch=arm64"}) || !slices.Equal(job.Prefers, []string{"cloud-linux"}) {
		t.Fatalf("runner labels = requires %v, prefers %v", job.Requires, job.Prefers)
	}
}

func TestRunnerLabelsCanBeCleared(t *testing.T) {
	plan := NewPlan()
	Job(plan, "build", func(context.Context) error { return nil }).
		Requires("gpu").Requires().Prefers("cloud-linux").Prefers()
	job := planDocument(plan).Jobs[0]
	if len(job.Requires) != 0 || len(job.Prefers) != 0 {
		t.Fatalf("cleared runner labels = requires %v, prefers %v", job.Requires, job.Prefers)
	}
}

func TestGroupPrefersAppliesToEveryMember(t *testing.T) {
	plan := NewPlan()
	a := Job(plan, "a", func(context.Context) error { return nil })
	b := Job(plan, "b", func(context.Context) error { return nil })
	GroupJobs(plan, "pair", a, b).Prefers("cloud-linux")
	jobs := planDocument(plan).Jobs
	if !slices.Equal(jobs[0].Prefers, []string{"cloud-linux"}) || !slices.Equal(jobs[1].Prefers, []string{"cloud-linux"}) {
		t.Fatalf("group preferences = %+v", jobs)
	}
}

func TestRunnerLabelDeclarationsLeaveNormalizationToTheHost(t *testing.T) {
	plan := NewPlan()
	Job(plan, "build", func(context.Context) error { return nil }).
		Requires("", " gpu ", "gpu", "gpu").Prefers("", " cloud-linux ", "cloud-linux")
	job := planDocument(plan).Jobs[0]
	if !slices.Equal(job.Requires, []string{"", " gpu ", "gpu", "gpu"}) || !slices.Equal(job.Prefers, []string{"", " cloud-linux ", "cloud-linux"}) {
		t.Fatalf("SDK changed runner declarations: requires %v, prefers %v", job.Requires, job.Prefers)
	}
}
