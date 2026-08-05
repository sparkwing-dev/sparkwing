package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenStepSequence struct{ sw.Base }

func (p GenStepSequence) ShortHelp() string { return "multi-step inner DAG generated pipeline" }

func (p GenStepSequence) Help() string { return p.ShortHelp() }

func (GenStepSequence) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run step-sequence"},
	}
}

func (GenStepSequence) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	sw.Job(plan, "release", &genStepSequenceJob{})
	return nil
}

type genStepSequenceJob struct{ sw.Base }

// Work builds the job's inner DAG. compile returns a typed value that
// sign and package read back with StepGet, so the version is computed
// once and shared across the steps.
func (j *genStepSequenceJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	compile := sw.Step(w, "compile", func(ctx context.Context) (string, error) {
		version := "v1.4.2"
		sw.Annotate(ctx, "compiled "+version)
		return version, nil
	})

	sign := sw.Step(w, "sign", func(ctx context.Context) error {
		version := sw.StepGet[string](ctx, compile)
		sw.Info(ctx, "signing %s", version)
		return nil
	}).Needs(compile)

	sw.Step(w, "package", func(ctx context.Context) error {
		version := sw.StepGet[string](ctx, compile)
		sw.Info(ctx, "packaging %s", version)
		return nil
	}).Needs(sign)

	return nil, nil
}

func init() {
	sw.Register[sw.NoInputs]("step-sequence", func() sw.Pipeline[sw.NoInputs] { return &GenStepSequence{} })
}
