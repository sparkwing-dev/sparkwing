package nodehost

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestSDKPlanningIntakeSecretDefaultsAreMaskedBeforeLogRelease(t *testing.T) {
	checkIntakeSecretDefault(t, "private-intake-default")
}

func FuzzSDKPlanningIntakeSecretDefaults(f *testing.F) {
	for _, value := range []string{"private-default", "42", "λ\\\"\n"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if value == "" || len(value) > 128 || strings.ToValidUTF8(value, "�") != value {
			t.Skip()
		}
		checkIntakeSecretDefault(t, value)
	})
}

func checkIntakeSecretDefault(t *testing.T, value string) {
	t.Helper()
	planner, input, fixture, _ := sdkPlanningFixture(t)
	input.SecretArgs = []string{"token"}
	var logs strings.Builder
	planner.Log = func(_ context.Context, frame nodeproto.FrameLog) error { logs.WriteString(frame.Message); return nil }
	start := planner.Start
	planner.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		if err := log(ctx, nodeproto.FrameLog{Message: value}); err != nil {
			return nil, err
		}
		process, err := start(ctx, input, log)
		if err != nil {
			return process, err
		}
		scripted := process.(*executorProcess)
		answer := scripted.answer
		scripted.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpDescribe {
				return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Default: &value}}}}}, nil
			}
			return answer(ctx, request)
		}
		return process, nil
	}
	prepared, err := planner.Prepare(t.Context(), input, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Args["token"] != value || planner.Masker.Mask(value) != "***" {
		t.Fatal("intake classification lost its SDK default")
	}
	if logs.String() != "***" {
		t.Fatal("describe log released an intake-secret default")
	}
}
