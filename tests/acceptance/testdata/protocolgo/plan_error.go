package protocolgo

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const PlanErrorName = "protocol-go-plan-error"

type PlanErrorInputs struct {
	First  string `flag:"first" secret:"true" default:"plan-secret"`
	Second string `flag:"second" secret:"true" default:"plan-secret-tail"`
}

type PlanErrorPipeline struct{}

func (PlanErrorPipeline) Plan(_ context.Context, _ *sparkwing.Plan, in PlanErrorInputs, _ sparkwing.RunContext) error {
	return fmt.Errorf("declaration refused: %s %s", in.First, in.Second)
}
