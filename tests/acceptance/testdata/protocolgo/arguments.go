package protocolgo

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const ArgumentsName = "protocol-go-job-arguments"

type ArgumentsPipeline struct{}

func (ArgumentsPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "primary", &argumentJob[PrimaryArgs]{}).
		OnFailure("recovery", &argumentJob[RecoveryArgs]{})
	return nil
}

type PrimaryArgs struct {
	Count int `flag:"count" default:"3"`
}

type RecoveryArgs struct {
	Mode   string `flag:"mode" default:"rollback" enum:"rollback,notify"`
	Reason string `flag:"reason" required:"true"`
}

type argumentJob[T any] struct {
	sparkwing.Base
	sparkwing.WithArgs[T]
	sparkwing.Produces[T]
}

func (j *argumentJob[T]) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "run", func(ctx context.Context) (T, error) {
		return j.Args(ctx), nil
	}), nil
}
