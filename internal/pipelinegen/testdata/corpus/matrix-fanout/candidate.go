package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenMatrix struct{ sw.Base }

func (p GenMatrix) ShortHelp() string { return "matrix fan-out generated pipeline" }

func (p GenMatrix) Help() string { return p.ShortHelp() }

func (GenMatrix) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run gen-matrix"},
	}
}

func (GenMatrix) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", &genMatrixBuild{})
	sw.JobFanOut(plan, "publish",
		[]string{"linux-amd64", "linux-arm64", "darwin-arm64"},
		func(target string) (string, any) {
			return "publish-" + target, genMatrixPublish(target)
		}).Needs(build)
	return nil
}

type genMatrixBuild struct{ sw.Base }

func (j *genMatrixBuild) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "compile", func(ctx context.Context) error {
		sw.Info(ctx, "compiling")
		return nil
	})
	return nil, nil
}

func genMatrixPublish(target string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sw.Info(ctx, "publishing %s", target)
		return nil
	}
}

func init() {
	sw.Register[sw.NoInputs]("matrix-fanout", func() sw.Pipeline[sw.NoInputs] { return &GenMatrix{} })
}
