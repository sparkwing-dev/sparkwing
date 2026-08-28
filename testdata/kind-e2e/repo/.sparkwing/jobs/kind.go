package jobs

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type kindSuccess struct{ sparkwing.Base }

type runIDProof struct {
	sparkwing.Base
	sparkwing.Produces[string]
	runID string
}

func (j *runIDProof) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "emit-run-id", func(ctx context.Context) (string, error) {
		sparkwing.Info(ctx, "sparkwing-kind-e2e-success run_id=%s", j.runID)
		return j.runID, nil
	}), nil
}

func (kindSuccess) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "prove-controller-runner-logs", &runIDProof{runID: rc.RunID}).Requires("cluster")
	return nil
}

type kindSlow struct{ sparkwing.Base }

func (kindSlow) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "wait-for-cancel", func(ctx context.Context) error {
		sparkwing.Info(ctx, "sparkwing-kind-e2e-waiting-for-cancel")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Minute):
			return nil
		}
	}).Requires("cluster")
	return nil
}

func init() {
	sparkwing.Register("kind-success", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &kindSuccess{} })
	sparkwing.Register("kind-slow", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &kindSlow{} })
}
