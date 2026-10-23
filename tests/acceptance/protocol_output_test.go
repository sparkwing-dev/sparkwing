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
		recordLeaf(t)
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
		recordLeaf(t)
		result := nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "prior failure"}}
		reply := nodeproto.Reply{ID: "next", OK: true, Result: json.RawMessage(`{"outcome":"success","output":null}`)}
		if err := reply.Decode(&result); err != nil || result.Outcome != "success" || result.Error != nil || string(result.Output) != "null" {
			t.Fatalf("success inherited prior failure: %+v, %v", result, err)
		}
	})
}

func testGoSDKRequiredDescribeFields(t *testing.T) {
	for name, raw := range map[string]string{
		"pipelines":         `{}`,
		"pipeline-name":     `{"pipelines":[{"args":[]}]}`,
		"pipeline-args":     `{"pipelines":[{"name":"build"}]}`,
		"argument-name":     `{"pipelines":[{"name":"build","args":[{"type":"string","required":false}]}]}`,
		"argument-type":     `{"pipelines":[{"name":"build","args":[{"name":"target","required":false}]}]}`,
		"argument-required": `{"pipelines":[{"name":"build","args":[{"name":"target","type":"string"}]}]}`,
		"secret-name":       `{"pipelines":[{"name":"build","args":[],"secrets":[{}]}]}`,
		"environment-name":  `{"pipelines":[{"name":"build","args":[],"env_vars":[{}]}]}`,
		"example-command":   `{"pipelines":[{"name":"build","args":[],"examples":[{"comment":"build"}]}]}`,
		"risk-labels":       `{"pipelines":[{"name":"build","args":[],"risks_by_step":[{"job_id":"build","step_id":"run"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			recordLeaf(t)
			var describe nodeproto.DescribeDoc
			err := (nodeproto.Reply{ID: "describe", OK: true, Result: json.RawMessage(raw)}).Decode(&describe)
			if !errors.Is(err, nodeproto.ErrMalformed) {
				t.Fatalf("missing required describe field accepted: %s: %v", raw, err)
			}
		})
	}
	for _, raw := range []string{
		`{"pipelines":[]}`,
		`{"pipelines":[{"name":"build","args":[]}]}`,
		`{"pipelines":[{"name":"build","args":[{"name":"target","type":"string","required":false,"future":null}],"secrets":[{"name":"TOKEN"}],"env_vars":[{"name":"REGION"}],"examples":[{"comment":"","command":"build"}],"risks_by_step":[{"job_id":"build","step_id":"run","labels":[]}],"future":null}],"future":null}`,
	} {
		var describe nodeproto.DescribeDoc
		if err := (nodeproto.Reply{ID: "describe", OK: true, Result: json.RawMessage(raw)}).Decode(&describe); err != nil {
			t.Fatalf("valid describe with optional or additive fields refused: %s: %v", raw, err)
		}
	}
}

func testGoSDKProcessOutputEncodingFailure(t *testing.T) {
	recordLeaf(t)
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
		{name: "non-json-whitespace", raw: json.RawMessage("\u00a0null")},
		{name: "unavailable", err: &apierr.Error{Code: apierr.Unavailable, Message: "fixture unavailable"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordLeaf(t)
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
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		want any
	}{
		{name: "explicit-null", raw: json.RawMessage(`null`)},
		{name: "formatted-null", raw: json.RawMessage("\n\t null \r\n")},
		{name: "null-string", raw: json.RawMessage(`"null"`), want: "null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordLeaf(t)
			host := sparkwingtest.New(sparkwingtest.Options{
				Outputs: map[string]json.RawMessage{"output": test.raw},
				Latest: func(context.Context, string, string) (json.RawMessage, error) {
					return test.raw, nil
				},
				RunPipeline: func(context.Context, string, map[string]string) (json.RawMessage, error) {
					return test.raw, nil
				},
			})
			t.Cleanup(func() { _ = host.Close() })
			ctx := host.Context(t.Context())
			output, err := sparkwing.RunAndAwait[any, sparkwing.NoInputs](ctx, "child", "output")
			if err != nil || output != test.want {
				t.Fatalf("child output = %v, %v, want %v", output, err, test.want)
			}
			ref := sparkwing.RefToLastRun[any]("upstream", "output")
			if value := ref.Get(ctx); value != test.want {
				t.Fatalf("latest output = %v, want %v", value, test.want)
			}
			if value, present := ref.TryGet(ctx); value != test.want || present != (test.want != nil) {
				t.Errorf("latest optional output = (%v, %t), want (%v, %t)", value, present, test.want, test.want != nil)
			}
			if value, present := (sparkwing.Ref[any]{JobID: "output"}).TryGet(ctx); value != test.want || !present {
				t.Errorf("in-run optional output = (%v, %t), want (%v, true)", value, present, test.want)
			}
		})
	}
}
