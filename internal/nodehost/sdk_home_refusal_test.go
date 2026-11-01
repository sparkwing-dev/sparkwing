package nodehost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestSDKIsolatedHomeRefusalShowsRemedyWithoutAuthorText(t *testing.T) {
	p, input, fixture, _ := sdkPlanningFixture(t)
	const private = "unclassified-author-secret"
	var logs strings.Builder
	p.Log = func(_ context.Context, record nodeproto.FrameLog) error { logs.WriteString(record.Message); return nil }
	start := p.Start
	p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		process, err := start(ctx, in, log)
		if err != nil {
			return process, err
		}
		proc := process.(*executorProcess)
		answer := proc.answer
		proc.answer = func(ctx context.Context, req nodeproto.Request) (any, error) {
			if req.Op == nodeproto.OpDescribe {
				if err := log(ctx, nodeproto.FrameLog{Message: private}); err != nil {
					return nil, err
				}
				return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{
					Name:  input.Run.Pipeline,
					Error: &apierr.Error{Code: "isolated_home_required", Message: private, Hint: private, Details: map[string]any{"secret": private}},
				}}}, nil
			}
			return answer(ctx, req)
		}
		return process, nil
	}
	prepared, err := p.Prepare(t.Context(), input, fixture.input)
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != "isolated_home_required" || prepared.Plan != nil {
		t.Fatalf("isolated-home discovery refusal = %v", err)
	}
	if !strings.Contains(refusal.Message, `HOME="$(mktemp -d)" SPARKWING_HOME="$(mktemp -d)"`) {
		t.Fatalf("refusal hid the temporary-home remedy: %v", err)
	}
	if strings.Contains(err.Error()+logs.String()+refusal.Hint, private) || len(refusal.Details) != 0 {
		t.Fatal("isolated-home refusal released unclassified author text")
	}
}
