package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestGateTimeoutsApplyToDirectAndReleasePlans(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		pipeline sparkwing.Pipeline[sparkwing.NoInputs]
		want     time.Duration
	}{
		{name: "pre-commit", pipeline: &PreCommit{}, want: 40 * time.Minute},
		{name: "pre-push", pipeline: &PrePush{}, want: 30 * time.Minute},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			if err := testCase.pipeline.Plan(context.Background(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: testCase.name}); err != nil {
				t.Fatal(err)
			}
			if got := mustNode(t, plan, testCase.name).TimeoutDuration(); got != testCase.want {
				t.Errorf("direct gate timeout = %s, want %s", got, testCase.want)
			}
			if got := mustNode(t, releasePlan(t), "gate-"+testCase.name).TimeoutDuration(); got != testCase.want {
				t.Errorf("release gate timeout = %s, want %s", got, testCase.want)
			}
		})
	}
}
