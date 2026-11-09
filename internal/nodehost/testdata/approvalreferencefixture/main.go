package main

import (
	"context"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"

	"os"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

// The protocol permits approval read declarations that the Go builder cannot express.
// Use the SDK server and its real HTTP services for that accepted declaration.
type operations struct{}

func (operations) Context(ctx context.Context, _ nodewire.RunContext, _ nodewire.RunnerIdentity, services sdkserve.Services) context.Context {
	return sdkserve.WithServices(ctx, services)
}
func (operations) Document(any) nodewire.PlanDoc                { return document() }
func (operations) Arguments(any) []nodewire.ArgumentDescription { return nil }
func (operations) Describe(context.Context) (nodewire.DescribeDoc, error) {
	return nodewire.DescribeDoc{Pipelines: []nodewire.PipelineDescription{{Name: "p", Args: []nodewire.ArgumentDescription{}}}}, nil
}

func (operations) Plan(context.Context, string, map[string]string, nodewire.RunContext) (any, nodewire.PlanDoc, error) {
	return struct{}{}, document(), nil
}

func (operations) RunStep(context.Context, any, nodewire.Request) (nodewire.RunResult, error) {
	panic("approval cannot execute a step")
}

func (operations) Eval(ctx context.Context, _ any, _ nodewire.Request) (nodewire.EvalResult, error) {
	value, err := sdkserve.ServicesFrom(ctx).Latest(ctx, "upstream", "artifact", 0, false, false)
	return nodewire.EvalResult{Value: value}, err
}

func document() nodewire.PlanDoc {
	return nodewire.PlanDoc{Jobs: []nodewire.JobSpec{
		{ID: "build", Steps: []nodewire.StepSpec{{ID: "run"}}},
		{ID: "gate", Needs: []string{"build"}, Approval: &nodewire.ApprovalSpec{}, Closures: nodewire.ClosureSpec{SkipIf: []string{"guard"}}, References: []nodewire.PipelineReference{{Pipeline: "upstream", Job: "artifact"}}},
	}}
}
func main() { os.Exit(sdkserve.Main(sdkserve.Config{SDKVersion: "fixture", Operations: operations{}})) }
