package sparkwing

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

type refusedRecords struct{ sdkserve.Services }

func (refusedRecords) Emit(nodeproto.FrameLog) {
	panic(&apierr.Error{Code: apierr.Forbidden, Message: "record write refused"})
}

func TestRecordWriteFailuresPreserveCanonicalErrors(t *testing.T) {
	for _, record := range []string{"annotation", "summary"} {
		t.Run(record, func(t *testing.T) {
			plan := NewPlan()
			Job(plan, "j", func(ctx context.Context) error {
				if record == "annotation" {
					Annotate(ctx, Annotation{Message: "record"})
				} else {
					Summary(ctx, "record")
				}
				return nil
			})
			ops := sdkOperations{}
			ctx := ops.Context(t.Context(), nodeproto.RunContext{}, nodeproto.RunnerIdentity{}, refusedRecords{})
			result, err := ops.RunStep(ctx, plan, nodeproto.Request{Job: "j", Step: "run", Attempt: 1})
			if err != nil || result.Outcome != "failed" || result.Error == nil || result.Error.Code != apierr.Forbidden {
				t.Fatalf("body result: %+v, %v", result, err)
			}
		})
	}
}
