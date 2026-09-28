package sparkwing_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

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
