package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenDiscardedRef struct{ sw.Base }

func (p GenDiscardedRef) ShortHelp() string { return "pipeline that discards a Ref (anti-pattern)" }

func (p GenDiscardedRef) Help() string { return p.ShortHelp() }

func (GenDiscardedRef) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run discarded-ref"},
	}
}

type discardedRefOut struct {
	Tag string
}

func (GenDiscardedRef) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", &genDiscardedRefBuild{})

	_ = sw.RefTo[discardedRefOut](build)
	sw.Job(plan, "deploy", genDiscardedRefDeploy).Needs(build)
	return nil
}

type genDiscardedRefBuild struct {
	sw.Base
	sw.Produces[discardedRefOut]
}

func (j *genDiscardedRefBuild) Work(w *sw.Work) (*sw.WorkStep, error) {
	return sw.Step(w, "compile", func(ctx context.Context) (discardedRefOut, error) {
		return discardedRefOut{Tag: "app:latest"}, nil
	}), nil
}

func genDiscardedRefDeploy(ctx context.Context) error {
	sw.Info(ctx, "deploying")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("discarded-ref", func() sw.Pipeline[sw.NoInputs] { return &GenDiscardedRef{} })
}
