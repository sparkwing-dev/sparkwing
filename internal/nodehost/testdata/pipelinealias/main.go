package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	Target string `flag:"target" required:"true"`
}

type buildInputs struct {
	Image string `flag:"image" required:"true"`
}

type build struct {
	sparkwing.WithArgs[buildInputs]
}

func (*build) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "body", func(context.Context) error { return nil }), nil
}

type pipeline struct{}

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, run sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", &build{}).Env("INVOKED", run.Pipeline).Env("TARGET", in.Target)
	return nil
}

func main() {
	sparkwing.Register[inputs]("Deploy", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	sparkwing.Main()
}
