package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type cleanupBudgetProcess struct {
	ExecutorProcess
	t        *testing.T
	attempts int
}

func (p *cleanupBudgetProcess) Cleanup(ctx context.Context) error {
	for range 2 {
		command, cancel := context.WithTimeout(ctx, CleanupTimeout)
		if command.Err() != nil {
			p.t.Fatal("cleanup inherited exhausted budget")
		}
		<-command.Done()
		cancel()
		p.attempts++
	}
	return nil
}

func TestLifecycleCleanupCommandsHaveIndependentBudgets(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(map[bool]string{false: "executor", true: "owner"}[owner], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var process *cleanupBudgetProcess
				if owner {
					input, _, deps, base := evaluationFixture(t)
					base.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
						if request.Op == nodeproto.OpEval {
							return nodeproto.EvalResult{Value: json.RawMessage(`[]`)}, nil
						}
						return nil, nil
					}
					process = &cleanupBudgetProcess{ExecutorProcess: base, t: t}
					deps.Start = func(context.Context, EvaluationInput) (ExecutorProcess, error) { return process, nil }
					host, err := NewOwnerEvaluator(deps)
					if err != nil {
						t.Fatal(err)
					}
					_, failure, err := host.Expand(t.Context(), input)
					if err != nil || failure != nil {
						t.Fatalf("failure=%v error=%v", failure, err)
					}
				} else {
					input, _, deps, _ := executorFixture(t)
					start := deps.Start
					deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
						base, err := start(ctx, input, activity)
						process = &cleanupBudgetProcess{ExecutorProcess: base, t: t}
						return process, err
					}
					host, err := NewNodeExecutor(deps)
					if err != nil {
						t.Fatal(err)
					}
					if err := host.Execute(t.Context(), input); err != nil {
						t.Fatal(err)
					}
				}
				if process.attempts != 2 {
					t.Fatalf("cleanup attempts=%d", process.attempts)
				}
			})
		})
	}
}

func TestSDKBoundaryRejectsMalformedResults(t *testing.T) {
	for _, tc := range []struct {
		op   nodeproto.Op
		raw  string
		kind string
	}{
		{nodeproto.OpRunStep, `{"outcome":"failed","error":{"code":"","message":"bad"}}`, ""},
		{nodeproto.OpEval, `{"value":null,"error":{"code":"","message":"bad"}}`, "verify"},
		{nodeproto.OpEval, `{"value":false}`, "verify"},
		{nodeproto.OpEval, `{"value":null}`, ""},
	} {
		t.Run(string(tc.op)+tc.raw+tc.kind, func(t *testing.T) {
			process := &executorProcess{answer: func(context.Context, nodeproto.Request) (any, error) { return json.RawMessage(tc.raw), nil }}
			session := &executorSession{process: process}
			var err error
			if tc.op == nodeproto.OpEval {
				_, err = (&sdkCode{}).eval(t.Context(), session, NodeInput{}, "closure", tc.kind, nil, nil)
			} else {
				var result nodeproto.RunResult
				err = session.call(t.Context(), nodeproto.Request{Op: tc.op}, &result, "")
			}
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSDKBoundaryRejectsMissingClosureKindBeforeTransport(t *testing.T) {
	process := &executorProcess{answer: func(context.Context, nodeproto.Request) (any, error) {
		t.Fatal("eval reached transport without a closure kind")
		return nil, nil
	}}
	session := &executorSession{process: process}
	var result nodeproto.EvalResult
	if err := session.call(t.Context(), nodeproto.Request{Op: nodeproto.OpEval, Closure: "verify"}, &result, ""); err == nil {
		t.Fatal("missing kind was accepted")
	}
}
