package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestDefaultDispatchWaitTimeoutForPlan(t *testing.T) {
	tests := []struct {
		name string
		node func(*sparkwing.Plan) *sparkwing.JobNode
		want time.Duration
	}{
		{
			name: "unbounded node retains default",
			node: func(plan *sparkwing.Plan) *sparkwing.JobNode {
				return sparkwing.Job(plan, "job", func(context.Context) error { return nil })
			},
			want: DefaultDispatchWaitTimeout,
		},
		{
			name: "long node timeout extends watchdog",
			node: func(plan *sparkwing.Plan) *sparkwing.JobNode {
				return sparkwing.Job(plan, "job", func(context.Context) error { return nil }).
					Timeout(time.Hour)
			},
			want: time.Hour + dispatchTimeoutDrainMargin,
		},
		{
			name: "retry envelope includes attempts and backoff",
			node: func(plan *sparkwing.Plan) *sparkwing.JobNode {
				return sparkwing.Job(plan, "job", func(context.Context) error { return nil }).
					Timeout(10 * time.Minute).
					Retry(2, sparkwing.RetryBackoff(2*time.Minute))
			},
			want: 36*time.Minute + dispatchTimeoutDrainMargin,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			tt.node(plan)
			if got := defaultDispatchWaitTimeoutForPlan(plan); got != tt.want {
				t.Fatalf("defaultDispatchWaitTimeoutForPlan() = %s, want %s", got, tt.want)
			}
		})
	}
}
