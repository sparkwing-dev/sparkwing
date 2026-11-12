package jobs

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestQueuedLandOverlapsReviewAndTestsAfterFastChecks(t *testing.T) {
	doc := inspectPipeline(t, "Land", map[string]string{
		"queued": "true", "queued-main": strings.Repeat("a", 40), "brief": "Overlap queued checks",
	})
	jobs := map[string]jobDocument{}
	for _, job := range doc.Jobs {
		jobs[job.ID] = job
		if !job.Inline || job.ContinueOnError || job.Optional {
			t.Fatalf("queued prerequisite must judge this checkout and block on failure: %+v", job)
		}
	}
	fast, ok := jobs["fast"]
	if !ok {
		t.Fatal("queued land has no separate fast job")
	}
	if !slices.Equal(fast.Needs, []string{"preconditions"}) {
		t.Fatalf("fast dependencies = %v", fast.Needs)
	}
	if fast.Cores != float64(prePushReservationCores(runtime.NumCPU())) {
		t.Fatalf("fast reservation = %v", fast.Cores)
	}
	tier := inspectWork(t, &PrePush{})
	if !slices.EqualFunc(fast.Steps, tier.Steps, func(a, b stepDocument) bool {
		return a.ID == b.ID && slices.Equal(a.Needs, b.Needs) && a.Finally == b.Finally && a.ContinueOnError == b.ContinueOnError
	}) || fast.FailurePolicy != sparkwing.FailFast {
		t.Fatalf("fast job must contain exactly the current pre-push tier: %+v", fast)
	}
	checks := jobs["affected-tests"]
	if !slices.Equal(checks.Needs, []string{"policy", "fast"}) || checks.FailurePolicy != sparkwing.FailFast {
		t.Fatalf("review and tests must share a fail-fast job after policy and fast: %+v", checks)
	}
	if checks.Cores != lightJobCores+float64(goStepParallelism(affectedPushHost())) {
		t.Fatalf("combined reservation = %v", checks.Cores)
	}
	if len(checks.Steps) != 2 || !slices.ContainsFunc(checks.Steps, func(step stepDocument) bool { return step.ID == "review" }) || !slices.ContainsFunc(checks.Steps, func(step stepDocument) bool { return step.ID == "test" }) {
		t.Fatalf("review and affected test bodies must both run: %+v", checks.Steps)
	}
	for _, step := range checks.Steps {
		if len(step.Needs) != 0 || step.Finally || step.ContinueOnError {
			t.Fatalf("review and tests must start together and cancel on either failure: %+v", step)
		}
	}
	if _, present := jobs["review"]; present {
		t.Fatal("queued review must not also run in an independent job")
	}
	if !slices.Equal(jobs["land"].Needs, []string{"preconditions", "policy", "fast", "affected-tests"}) {
		t.Fatalf("push must join every prerequisite: %v", jobs["land"].Needs)
	}
}
