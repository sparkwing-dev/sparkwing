package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenMinimal struct{ sw.Base }

func (p GenMinimal) ShortHelp() string { return "minimal generated pipeline" }

func (p GenMinimal) Help() string { return p.ShortHelp() }

func (GenMinimal) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run gen-minimal"},
	}
}

func (GenMinimal) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	sw.Job(plan, run.Pipeline, &genMinimalJob{})
	return nil
}

type genMinimalJob struct{ sw.Base }

func (j *genMinimalJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func (genMinimalJob) run(ctx context.Context) error {
	sw.Info(ctx, "hello from the generated minimal pipeline")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("minimal", func() sw.Pipeline[sw.NoInputs] { return &GenMinimal{} })
}
