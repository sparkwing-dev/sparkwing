package nodehost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestSDKCheckoutPlanningUsesRegisteredNameAndRetainsGoType(t *testing.T) {
	planner, input, fixture, processes := sdkPlanningFixture(t)
	start := planner.Start
	planner.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		process, err := start(ctx, input, log)
		proc := process.(*executorProcess)
		answer := proc.answer
		proc.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			switch request.Op {
			case nodeproto.OpDescribe:
				return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "Deploy", Args: []nodeproto.ArgumentDescription{{Name: "target", Type: "string", Required: true}, {Name: "build.image", Type: "string", Required: true, JobID: "build"}}}}}, nil
			case nodeproto.OpPlan:
				if request.Pipeline != "Deploy" || request.Run.Pipeline != "deploy-prod" || request.Args["target"] != "production" || request.Args["build.image"] != "release" {
					t.Fatalf("pipeline identity or arguments changed: %+v", request)
				}
			}
			return answer(ctx, request)
		}
		return process, err
	}
	prepared, err := planner.PrepareCheckout(t.Context(), CheckoutPlanningInput{
		Request: runspec.SealedRequest{RunID: "run", Request: runspec.Request{Pipeline: "deploy-prod", RequestedArgs: map[string]string{"target": "production"}}},
		Entry:   input.Entry, Runner: input.Runner, Planning: fixture.input, ProjectFile: "sparkwing.yaml",
		Project: []byte("pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n    args: {build.image: release}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Resolved.Entrypoint != "Deploy" {
		t.Fatalf("resolved selection = %+v, cache = %+v", prepared.Resolved, prepared.Planning)
	}
	if _, err := PlanInitial(t.Context(), prepared.Planning, prepared.Plan); err != nil {
		t.Fatal(err)
	}
	for _, process := range *processes {
		if process.closed != 1 || process.cleaned != 1 {
			t.Fatal("alias planning leaked a process")
		}
	}
}

func TestNodeExecutorReplanUsesRegisteredName(t *testing.T) {
	input, state, deps, _ := executorFixture(t)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Request.Request.Pipeline, spec.Resolved.Entrypoint = "deploy-prod", "Deploy"
	input.Accepted.Spec, _ = json.Marshal(spec)
	state.accepted = &input.Accepted
	start := deps.Start
	planned := false
	deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, input, activity)
		process.(*executorProcess).answer = func(_ context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpPlan {
				planned = true
				if request.Pipeline != "Deploy" || request.Run.Pipeline != "deploy-prod" {
					t.Errorf("replan changed pipeline identity: %+v", request)
				}
			}
			return nil, nil
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if !planned {
		t.Fatal("accepted alias did not replan")
	}
}
