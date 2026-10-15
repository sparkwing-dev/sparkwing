package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKReplyResultReuse(t *testing.T) {
	t.Run("hello-required-fields", func(t *testing.T) {
		var greeting nodeproto.HelloResult
		complete := nodeproto.Reply{ID: "first", OK: true, Result: json.RawMessage(`{"protocol":2,"sdk":{"language":"go","version":"v1"},"features":[]}`)}
		if err := complete.Decode(&greeting); err != nil {
			t.Fatal(err)
		}
		incomplete := nodeproto.Reply{ID: "second", OK: true, Result: json.RawMessage(`{"protocol":2,"sdk":{"language":"go"}}`)}
		if err := incomplete.Decode(&greeting); !errors.Is(err, nodeproto.ErrMalformed) {
			t.Fatalf("incomplete hello inherited required fields from prior reply: %+v, %v", greeting, err)
		}
	})
	t.Run("success-after-failure", func(t *testing.T) {
		result := nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "prior failure"}}
		reply := nodeproto.Reply{ID: "next", OK: true, Result: json.RawMessage(`{"outcome":"success","output":null}`)}
		if err := reply.Decode(&result); err != nil || result.Outcome != "success" || result.Error != nil || string(result.Output) != "null" {
			t.Fatalf("success inherited prior failure: %+v, %v", result, err)
		}
	})
}

func testGoSDKProcessOutputEncodingFailure(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	plan := sparkwing.NewPlan()
	if err := (protocolgo.Pipeline{}).Plan(t.Context(), plan, protocolgo.Inputs{Name: "demo"}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	_, err := host.RunStep(t.Context(), plan, "invalid-output", "run")
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.SDKError || !strings.Contains(failure.Message, "NaN") {
		t.Fatalf("output encoding failure is not a canonical body failure: %v", err)
	}
	actual, err := acceptance.RunGoSDKProcessFixture(t.Context(), "", "invalid-output")
	if err != nil {
		t.Fatal(err)
	}
	got := actual.Steps["invalid-output"]
	if got.Outcome != "failed" || got.Error == nil || got.Error.Code != apierr.SDKError || len(got.Output) != 0 {
		t.Fatalf("output encoding failure = %+v", got)
	}
	if failure.Code != got.Error.Code || failure.Message != got.Error.Message {
		t.Fatalf("fake and process output encoding failures differ: fake=%+v process=%+v", failure, got.Error)
	}
}

func testGoSDKOutputFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		err  error
	}{
		{name: "missing"},
		{name: "corrupt", raw: json.RawMessage(`{"broken":`)},
		{name: "unavailable", err: &apierr.Error{Code: apierr.Unavailable, Message: "fixture unavailable"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := sparkwingtest.New(sparkwingtest.Options{
				Latest:      func(context.Context, string, string) (json.RawMessage, error) { return test.raw, test.err },
				RunPipeline: func(context.Context, string, map[string]string) (json.RawMessage, error) { return test.raw, test.err },
			})
			t.Cleanup(func() { _ = host.Close() })
			ctx := host.Context(t.Context())
			ref := sparkwing.RefToLastRun[any]("upstream", "output")
			func() {
				defer func() {
					if recover() == nil {
						t.Error("required reference read succeeded without valid output")
					}
				}()
				ref.Get(ctx)
			}()
			if test.name == "missing" {
				if _, present := ref.TryGet(ctx); present {
					t.Error("missing optional output was present")
				}
			}
			_, err := sparkwing.RunAndAwait[any, sparkwing.NoInputs](ctx, "child", "output")
			if err == nil || test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("child output error = %v, underlying = %v", err, test.err)
			}
		})
	}
	t.Run("explicit-null", func(t *testing.T) {
		host := sparkwingtest.New(sparkwingtest.Options{RunPipeline: func(context.Context, string, map[string]string) (json.RawMessage, error) {
			return json.RawMessage(`null`), nil
		}})
		t.Cleanup(func() { _ = host.Close() })
		output, err := sparkwing.RunAndAwait[any, sparkwing.NoInputs](host.Context(t.Context()), "child", "output")
		if err != nil || output != nil {
			t.Fatalf("explicit null output = %v, %v", output, err)
		}
	})
}
