package nodehost

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestOldSDKPlanFailureNamesUpgradeWithoutExposingPanic(t *testing.T) {
	for _, version := range []string{"v0.1.0", "v0.2.0", "v0.3.0"} {
		t.Run(version, func(t *testing.T) {
			planner, input, _, _ := sdkPlanningFixture(t)
			input.Entry.Engine.Identity.Pin = engineid.Pin{SDK: engineid.GoSDK, Version: version}
			failure := planner.planningError(context.Background(), nil, &apierr.Error{Code: apierr.PlanFailed, Message: "runtime error: invalid memory address or nil pointer dereference"}, false, input)
			if strings.Contains(failure.Error(), "invalid memory address") {
				t.Fatal(failure)
			}
			upgrade := strings.Contains(failure.Error(), "sparkwing pipeline upgrade")
			if upgrade != (version != "v0.3.0") {
				t.Fatalf("SDK %s diagnostic: %v", version, failure)
			}
		})
	}
}

func TestOldSDKOrdinaryPlanFailureDoesNotSuggestUpgrade(t *testing.T) {
	for _, message := range []string{"missing deployment setting", "panic: invalid user argument", "nil Git configuration"} {
		planner, input, _, _ := sdkPlanningFixture(t)
		input.Entry.Engine.Identity.Pin = engineid.Pin{SDK: engineid.GoSDK, Version: "v0.2.0"}
		failure := planner.planningError(t.Context(), nil, &apierr.Error{Code: apierr.PlanFailed, Message: message}, false, input)
		if strings.Contains(failure.Error(), "sparkwing pipeline upgrade") {
			t.Fatalf("ordinary Plan error suggests SDK upgrade: %v", failure)
		}
	}
}

func TestOldSDKDiscoveryHintClassifiesTheOriginalFailure(t *testing.T) {
	for _, message := range []string{"runtime error: invalid memory address or nil pointer dereference", "missing deployment setting"} {
		t.Run(message, func(t *testing.T) {
			planner, input, _, _ := sdkPlanningFixture(t)
			input.Entry.Engine.Identity.Pin = engineid.Pin{SDK: engineid.GoSDK, Version: "v0.2.0"}
			input.Entrypoint = input.Run.Pipeline
			planner.EnsureEngine = func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
				return input.Entry.Engine, nil
			}
			start := planner.Start
			planner.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				if err != nil {
					return process, err
				}
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodewire.Request) (any, error) {
					if req.Op == nodewire.OpHello {
						return nodewire.HelloResult{Protocol: nodewire.Version, SDK: nodewire.SDKIdentity{Language: "go", Version: "v0.2.0"}}, nil
					}
					if req.Op == nodewire.OpDescribe {
						return nodewire.DescribeDoc{Pipelines: []nodewire.PipelineDescription{{Name: input.Entrypoint, Args: []nodewire.ArgumentDescription{}, Error: &apierr.Error{Code: apierr.PlanFailed, Message: message}}}}, nil
					}
					return answer(ctx, req)
				}
				return process, nil
			}
			_, err := planner.Inspect(t.Context(), input)
			if err == nil || strings.Contains(err.Error(), message) {
				t.Fatalf("discovery failure was not withheld: %v", err)
			}
			if got, want := strings.Contains(err.Error(), "sparkwing pipeline upgrade"), strings.HasPrefix(message, "runtime error:"); got != want {
				t.Fatalf("upgrade hint = %v, want %v: %v", got, want, err)
			}
		})
	}
}
