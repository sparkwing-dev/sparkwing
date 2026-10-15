package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNodeProtocolEvaluatesCallbacks(t *testing.T) {
	plan := sparkwing.NewPlan()
	var after error
	sparkwing.Job(plan, "gated", func(context.Context) error { return nil }).
		SkipIf(func(context.Context) bool { return true }).
		SkipIf(func(context.Context) bool { panic("skip_if blew up") }).
		BeforeRun(func(context.Context) error { return errors.New("") }).
		Verify(func(context.Context) error { return nil }).
		AfterRun(func(_ context.Context, err error) { after = err }).
		Memo(func(context.Context) (sparkwing.MemoValue, error) { return "key", nil })
	sparkwing.Job(plan, "no-memo", func(context.Context) error { return nil }).Memo(func(context.Context) (sparkwing.MemoValue, error) { return sparkwing.NoMemo, nil })
	sparkwing.Job(plan, "key-error", func(context.Context) error { return nil }).Memo(func(context.Context) (sparkwing.MemoValue, error) { return "", errors.New("no lockfile") })
	sparkwing.Job(plan, "empty-key", func(context.Context) error { return nil }).Memo(func(context.Context) (sparkwing.MemoValue, error) { return "", nil })
	ops := sdkserve.DefaultOperations
	for _, tc := range []struct {
		closure, value, message string
		failed                  bool
		input                   json.RawMessage
	}{
		{closure: "gated/skip/0", value: "true"},
		{closure: "gated/skip/1", value: "null", failed: true, message: "skip_if blew up"},
		{closure: "gated/before/0", value: "null", failed: true},
		{closure: "gated/verify/0", value: "null"},
		{closure: "gated/after/0", value: "null", input: json.RawMessage(`{"outcome":"failed","error":{"code":"sdk_error","message":"health check: 503"}}`)},
		{closure: "gated/memo/0", value: `"key"`},
		{closure: "no-memo/memo/0", value: "null"},
		{closure: "key-error/memo/0", value: "null", failed: true, message: "no lockfile"},
		{closure: "empty-key/memo/0", value: "null", failed: true, message: "must not be empty"},
	} {
		t.Run(tc.closure, func(t *testing.T) {
			result, err := ops.Eval(t.Context(), plan, nodeproto.Request{Closure: tc.closure, Input: tc.input})
			if err != nil || string(result.Value) != tc.value || (result.Error != nil) != tc.failed || result.Error != nil && !strings.Contains(result.Error.Message, tc.message) {
				t.Fatalf("eval: %+v, %v", result, err)
			}
		})
	}
	if after == nil || after.Error() != "health check: 503" {
		t.Fatalf("after_run received %v", after)
	}
	result, err := ops.Eval(t.Context(), plan, nodeproto.Request{Closure: "gated/after/0"})
	if err != nil || result.Error == nil {
		t.Fatalf("after_run without result: %+v, %v", result, err)
	}
	_, err = ops.Eval(t.Context(), plan, nodeproto.Request{Closure: "gated/skip/9"})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.UnknownClosure {
		t.Fatalf("unknown closure: %v", err)
	}
}

func TestNodeProtocolEvaluatesWhileAStepRuns(t *testing.T) {
	released := make(chan struct{})
	started := make(chan struct{})
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "gated", func(ctx context.Context) error {
		close(started)
		select {
		case <-released:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}).SkipIf(func(context.Context) bool { close(released); return true })
	ops := sdkserve.DefaultOperations
	type execution struct {
		result nodeproto.RunResult
		err    error
	}
	done := make(chan execution, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		result, err := ops.RunStep(ctx, plan, nodeproto.Request{Job: "gated", Step: "run", Attempt: 1, Settled: []string{}})
		done <- execution{result, err}
	}()
	<-started
	evaluation, err := ops.Eval(ctx, plan, nodeproto.Request{Closure: "gated/skip/0"})
	if err != nil || string(evaluation.Value) != "true" {
		t.Fatalf("evaluation: %+v, %v", evaluation, err)
	}
	run := <-done
	if run.err != nil || run.result.Outcome != "success" {
		t.Fatalf("step: %+v, %v", run.result, run.err)
	}
}
