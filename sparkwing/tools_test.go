package sparkwing_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNeedsToolsAddsToolTermsThatSurviveRequires(t *testing.T) {
	plan := sparkwing.NewPlan()
	job := sparkwing.Job(plan, "apply", func(ctx context.Context) error { return nil }).
		NeedsTools("terraform", "terraform").Requires("linux")
	if got := job.RequiresLabels(); !slices.Equal(got, []string{"linux", "tool:terraform"}) {
		t.Fatalf("selector = %v, want linux and tool:terraform once", got)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), `NeedsTools("terraformm")`) {
			t.Fatalf("an unknown tool = %v, want a panic naming it", r)
		}
	}()
	job.NeedsTools("terraformm")
}

func TestClaimWaitIsThePlansOwn(t *testing.T) {
	plan := sparkwing.NewPlan()
	if plan.ClaimWaitValue() != 0 {
		t.Fatal("a new plan sets a claim wait")
	}
	if plan.ClaimWait(2*time.Hour).ClaimWaitValue() != 2*time.Hour {
		t.Fatal("ClaimWait did not stick")
	}
}

func TestNeedsToolsReachesADynamicGroupsGeneratedMembers(t *testing.T) {
	plan := sparkwing.NewPlan()
	src := sparkwing.Job(plan, "discover", &discoverJob{items: []string{"a"}})
	sparkwing.JobFanOutDynamic(plan, "builds", src, func(s string) (string, any) {
		return "build-" + s, func(ctx context.Context) error { return nil }
	}).NeedsTools("terraform")
	ctx := sparkwingruntime.WithJSONResolver(context.Background(), func(id string) ([]byte, bool) {
		return []byte(`["a","b"]`), id == "discover"
	})
	children := plan.Expansions()[0].Gen(ctx)
	if len(children) != 2 {
		t.Fatalf("generated %d members, want 2", len(children))
	}
	for _, c := range children {
		if !slices.Equal(c.RequiresLabels(), []string{"tool:terraform"}) {
			t.Fatalf("member %s selector = %v, want tool:terraform", c.ID(), c.RequiresLabels())
		}
	}
}

func TestGroupNeedsToolsRejectsAnUnknownToolWhenCalled(t *testing.T) {
	plan := sparkwing.NewPlan()
	src := sparkwing.Job(plan, "discover", &discoverJob{items: []string{"a"}})
	group := sparkwing.JobFanOutDynamic(plan, "builds", src, func(s string) (string, any) {
		return "build-" + s, func(ctx context.Context) error { return nil }
	})
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), `JobGroup.NeedsTools("terraformm")`) {
			t.Fatalf("an unknown tool on a dynamic group = %v, want a panic at the call", r)
		}
	}()
	group.NeedsTools("terraformm")
}
