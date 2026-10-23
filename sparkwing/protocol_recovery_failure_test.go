package sparkwing

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestRecoveryReceivesFailureStageAndError(t *testing.T) {
	plan := NewPlan()
	calls := 0
	Job(plan, "deploy", func(context.Context) error { return nil }).OnFailure("rollback", func(_ context.Context, failure Failure) error {
		calls++
		var cause *apierr.Error
		if failure.Stage != StageVerify || !errors.As(failure.Err, &cause) || cause.Code != apierr.SDKError || cause.Message != "health check failed" {
			t.Errorf("failure context = %+v", failure)
		}
		return nil
	})
	got, err := (sdkOperations{}).RunStep(t.Context(), plan, nodeproto.Request{Job: "rollback", Step: "run", Attempt: 1, Settled: []string{}, Failure: &nodeproto.JobFailure{Job: "deploy", Stage: "verify", Error: &apierr.Error{Code: apierr.SDKError, Message: "health check failed"}}})
	if err != nil || got.Error != nil || got.Outcome != "success" || calls != 1 {
		t.Fatalf("recovery = %+v, %v, calls %d", got, err, calls)
	}
}
