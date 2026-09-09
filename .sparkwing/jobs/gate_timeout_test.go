package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestGateTimeoutsApplyToDirectAndReleasePlans(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pipeline sparkwing.Pipeline[sparkwing.NoInputs]
		want     time.Duration
	}{
		{name: "pre-commit", pipeline: &PreCommit{}, want: 40 * time.Minute},
		{name: "pre-push", pipeline: &PrePush{}, want: 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			if err := tc.pipeline.Plan(context.Background(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: tc.name}); err != nil {
				t.Fatal(err)
			}
			if got := mustNode(t, plan, tc.name).TimeoutDuration(); got != tc.want {
				t.Errorf("direct gate timeout = %s, want %s", got, tc.want)
			}
			if got := mustNode(t, releasePlan(t), "gate-"+tc.name).TimeoutDuration(); got != tc.want {
				t.Errorf("release gate timeout = %s, want %s", got, tc.want)
			}
		})
	}
}
