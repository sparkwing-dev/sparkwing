package protocolgo

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const ClosureName = "protocol-go-closure-identifiers"

type ClosurePipeline struct{}

func (ClosurePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	for _, id := range []string{"scope", "scope/step/release", "scope%2Fstep%2Frelease"} {
		condition := id == "scope/step/release"
		job := sparkwing.Job(plan, id, closureSteps{}).
			SkipIf(func(context.Context) bool { return condition })
		job.Generate(func(context.Context, *sparkwing.Plan) error { return nil })
	}
	return nil
}

type closureSteps struct{ sparkwing.Base }

func (closureSteps) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	for _, id := range []string{"release", "release/check", "release%2Fcheck"} {
		condition := id == "release/check"
		sparkwing.Step(work, id, func(context.Context) error { return nil }).
			SkipIf(func(context.Context) bool { return condition })
	}
	return nil, nil
}
