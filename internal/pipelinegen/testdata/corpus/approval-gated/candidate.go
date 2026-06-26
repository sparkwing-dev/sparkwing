package jobs

import (
	"context"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenApproval struct{ sw.Base }

func (p GenApproval) ShortHelp() string { return "approval-gated generated pipeline" }

func (p GenApproval) Help() string { return p.ShortHelp() }

func (GenApproval) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run gen-approval"},
	}
}

func (GenApproval) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", &genApprovalJob{name: "build"})
	gate := sw.JobApproval(plan, "approve-deploy", sw.ApprovalConfig{
		Message:  "Promote build to prod?",
		Timeout:  20 * time.Second,
		OnExpiry: sw.ApprovalApprove,
	}).Needs(build)
	sw.Job(plan, "deploy", &genApprovalJob{name: "deploy"}).Needs(gate)
	return nil
}

type genApprovalJob struct {
	sw.Base
	name string
}

func (j *genApprovalJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", func(ctx context.Context) error {
		sw.Info(ctx, "running %s", j.name)
		return nil
	})
	return nil, nil
}

func init() {
	sw.Register[sw.NoInputs]("approval-gated", func() sw.Pipeline[sw.NoInputs] { return &GenApproval{} })
}
