package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

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
