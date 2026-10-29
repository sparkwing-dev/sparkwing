package sparkwing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestAfterRunConsumesFinalResult(t *testing.T) {
	for _, input := range []string{`{"outcome":"success"}`, `{"outcome":"failed"}`, `{"outcome":"unknown"}`, `null`, `{}`} {
		t.Run(input, func(t *testing.T) {
			plan := NewPlan()
			calls := 0
			Job(plan, "job", func(context.Context) error { return nil }).AfterRun(func(_ context.Context, err error) {
				calls++
				if err != nil {
					t.Errorf("success callback received error: %v", err)
				}
			})
			result, err := (sdkOperations{}).Eval(t.Context(), plan, nodeproto.Request{Job: "job", Closure: "job/after/0", Input: json.RawMessage(input)})
			if err != nil {
				t.Fatal(err)
			}
			if input == `{"outcome":"success"}` {
				if calls != 1 || result.Error != nil || string(result.Value) != "null" {
					t.Fatalf("successful final result: calls=%d result=%+v", calls, result)
				}
			} else if calls != 0 || result.Error == nil {
				t.Fatalf("invalid final result reached callback: calls=%d result=%+v", calls, result)
			}
		})
	}
}

func TestAfterRunPanicReportsAnErrorThatFailsTheNode(t *testing.T) {
	plan := NewPlan()
	Job(plan, "job", func(context.Context) error { return nil }).AfterRun(func(context.Context, error) {
		panic("report upload failed")
	})
	result, err := (sdkOperations{}).Eval(t.Context(), plan, nodeproto.Request{Job: "job", Closure: "job/after/0", Input: json.RawMessage(`{"outcome":"success"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == nil {
		t.Fatalf("a panicking AfterRun hook reported success: %+v", result)
	}
}
