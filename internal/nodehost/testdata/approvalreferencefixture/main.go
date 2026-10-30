package main

import (
	"context"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

// The protocol permits approval read declarations that the Go builder cannot express.
// Use the SDK server and its real HTTP services for that accepted declaration.
type operations struct{}

func (operations) Context(ctx context.Context, _ nodeproto.RunContext, _ nodeproto.RunnerIdentity, services sdkserve.Services) context.Context {
	return sdkserve.WithServices(ctx, services)
}
func (operations) Document(any) nodeproto.PlanDoc                { return document() }
func (operations) Arguments(any) []nodeproto.ArgumentDescription { return nil }
func (operations) Describe(context.Context) (nodeproto.DescribeDoc, error) {
	return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "p", Args: []nodeproto.ArgumentDescription{}}}}, nil
}

func (operations) Plan(context.Context, string, map[string]string, nodeproto.RunContext) (any, nodeproto.PlanDoc, error) {
	return struct{}{}, document(), nil
}

func (operations) RunStep(context.Context, any, nodeproto.Request) (nodeproto.RunResult, error) {
	panic("approval cannot execute a step")
}

func (operations) Eval(ctx context.Context, _ any, _ nodeproto.Request) (nodeproto.EvalResult, error) {
	value, err := sdkserve.ServicesFrom(ctx).Latest(ctx, "upstream", "artifact", 0, false)
	return nodeproto.EvalResult{Value: value}, err
}

func document() nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "build", Steps: []nodeproto.StepSpec{{ID: "run"}}},
		{ID: "gate", Needs: []string{"build"}, Approval: &nodeproto.ApprovalSpec{}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"guard"}}, References: []nodeproto.PipelineReference{{Pipeline: "upstream", Job: "artifact"}}},
	}}
}
func main() { os.Exit(sdkserve.Main(sdkserve.Config{SDKVersion: "fixture", Operations: operations{}})) }
