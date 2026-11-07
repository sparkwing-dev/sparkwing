package sparkwing

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestNeedsToolsKeepsRequiredLabels(t *testing.T) {
	plan := NewPlan()
	Job(plan, "apply", func(context.Context) error { return nil }).
		NeedsTools("terraform", "terraform").Requires("linux")
	if got := planDocument(plan).Jobs[0].Requires; !slices.Equal(got, []string{"linux", "tool:terraform"}) {
		t.Fatalf("required labels = %v", got)
	}
}

func TestClaimWaitPreservesExplicitValue(t *testing.T) {
	plan := NewPlan().ClaimWait(2 * time.Hour)
	value := planDocument(plan).ClaimWaitMS
	if value == nil || *value != (2*time.Hour).Milliseconds() {
		t.Fatalf("claim wait = %v", value)
	}
}

func TestGroupNeedsToolsRelaysNewToolNames(t *testing.T) {
	plan := NewPlan()
	job := Job(plan, "build", func(context.Context) error { return nil })
	GroupJobs(plan, "builds", job).NeedsTools("host-added-tool")
	if got := planDocument(plan).Jobs[0].Requires; !slices.Contains(got, "tool:host-added-tool") {
		t.Fatalf("selectors=%v", got)
	}
}
