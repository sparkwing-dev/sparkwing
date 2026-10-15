package main

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	Name string `flag:"name" required:"true"`
}

type pipeline struct{ sparkwing.Base }

func (pipeline) ShortHelp() string { return "Node protocol v2 conformance" }

func (pipeline) Plan(ctx context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	if in.Name == "refuse" {
		return fmt.Errorf("fixture plan refused")
	}
	sparkwing.Info(ctx, "planning %s", in.Name)
	job := sparkwing.Job(plan, "task", func(ctx context.Context) error {
		sparkwing.Info(ctx, "running %s", in.Name)
		if in.Name == "fail-body" {
			return fmt.Errorf("fixture body failed")
		}
		return nil
	})
	job.BeforeRun(func(context.Context) error {
		if in.Name == "fail-eval" {
			return fmt.Errorf("fixture hook failed")
		}
		return nil
	})
	job.Verify(func(context.Context) error { return nil })
	job.AfterRun(func(context.Context, error) {})
	job.SkipIf(func(context.Context) bool { return false })
	job.Memo(func(context.Context) (sparkwing.MemoValue, error) { return sparkwing.MemoValue("fixture-key"), nil })
	job.Generate(func(_ context.Context, expansion *sparkwing.Plan) error {
		sparkwing.Job(expansion, "generated", func(context.Context) error { return nil })
		return nil
	})
	return nil
}

func main() {
	sparkwing.Register[inputs]("conformance", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	sparkwing.Main()
}
