package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func evalGoSDKMemo(t *testing.T, resolve sparkwing.MemoKeyFn) (json.RawMessage, error) {
	t.Helper()
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "memo-probe", func(context.Context) error { return nil }).Memo(resolve)
	host := sparkwingtest.New(sparkwingtest.Options{})
	defer func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	}()
	doc := sdkserve.DefaultOperations.Document(plan)
	return host.Eval(t.Context(), plan, "memo-probe", doc.Jobs[0].Closures.MemoKey, nil)
}

func testGoSDKMemoInputBoundaries(t *testing.T) {
	t.Run("recipe-transport", func(t *testing.T) {
		recordLeaf(t)
		plan := sparkwing.NewPlan()
		recipe := inputs.Compose(inputs.Files("["), inputs.Tree("missing"), inputs.Env("MODE"), inputs.Parts("version", 2))
		sparkwing.Job(plan, "memo-probe", func(context.Context) error { return nil }).Memo(recipe)
		job := sdkserve.DefaultOperations.Document(plan).Jobs[0]
		if job.Memo == nil || job.Memo.Input == nil || job.Closures.MemoKey != "" {
			t.Fatalf("missing host recipe: %+v", job)
		}
	})
	t.Run("callback-values", func(t *testing.T) {
		recordLeaf(t)
		for _, value := range []sparkwing.MemoValue{sparkwing.NoMemo, sparkwing.MemoKey("fixed", map[string]any{"v": 2})} {
			raw, err := evalGoSDKMemo(t, func(context.Context) (sparkwing.MemoValue, error) { return value, nil })
			var want []byte
			if value == sparkwing.NoMemo {
				want = []byte("null")
			} else {
				want, _ = json.Marshal(value)
			}
			if err != nil || string(raw) != string(want) {
				t.Fatalf("memo callback value = %s, %v; want %s", raw, err, want)
			}
		}
	})
	t.Run("callback-error", func(t *testing.T) {
		recordLeaf(t)
		cause := errors.New("author input failed")
		if _, err := evalGoSDKMemo(t, func(context.Context) (sparkwing.MemoValue, error) { return nil, cause }); err == nil {
			t.Fatal("callback error lost")
		}
	})
}
