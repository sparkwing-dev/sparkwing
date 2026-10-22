package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type pipeline struct{ sparkwing.Base }

type buildJob struct{ sparkwing.Produces[bool] }

func (buildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (bool, error) { return true, nil }), nil
}

func (pipeline) Plan(_ context.Context, p *sparkwing.Plan, _ struct{}, _ sparkwing.RunContext) error {
	build := sparkwing.Job(p, "build", buildJob{})
	ref := sparkwing.RefTo[bool](build)
	p.Approval("gate", sparkwing.ApprovalConfig{}).Needs(build).SkipIf(func(ctx context.Context) bool { return ref.Get(ctx) })
	return nil
}

func main() {
	sparkwing.Register[struct{}]("p", func() sparkwing.Pipeline[struct{}] { return pipeline{} })
	sparkwing.Main()
}
