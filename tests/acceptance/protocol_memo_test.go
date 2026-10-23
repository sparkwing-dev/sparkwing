package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
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
	for _, test := range []struct {
		name    string
		resolve sparkwing.MemoKeyFn
	}{
		{"invalid-glob", inputs.Files("[")},
		{"invalid-ignore", inputs.RepoFiles(inputs.Ignore("["))},
		{"empty-constant", inputs.Const("")},
		{"composed-error", inputs.Compose(inputs.Const("valid"), func(context.Context) (sparkwing.MemoValue, error) {
			return "", errors.New("memo input failed")
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordLeaf(t)
			raw, err := evalGoSDKMemo(t, test.resolve)
			var failure *apierr.Error
			if len(raw) != 0 || !errors.As(err, &failure) || failure.Code != apierr.SDKError {
				t.Fatalf("invalid memo input produced %s, %v", raw, err)
			}
		})
	}
	t.Run("no-memo", func(t *testing.T) {
		recordLeaf(t)
		calls := 0
		resolve := inputs.Compose(func(context.Context) (sparkwing.MemoValue, error) {
			return sparkwing.NoMemo, nil
		}, func(context.Context) (sparkwing.MemoValue, error) {
			calls++
			return "unexpected-key", nil
		})
		if raw, err := evalGoSDKMemo(t, resolve); err != nil || string(raw) != "null" || calls != 0 {
			t.Fatalf("memo bypass = %s, %v; later input calls = %d", raw, err, calls)
		}
	})
	t.Run("fingerprints", func(t *testing.T) {
		recordLeaf(t)
		assertKey := func(resolve sparkwing.MemoKeyFn, want string) {
			t.Helper()
			raw, err := evalGoSDKMemo(t, resolve)
			var key string
			if err != nil || json.Unmarshal(raw, &key) != nil || key != want {
				t.Fatalf("memo key = %s, %v; want %q", raw, err, want)
			}
		}
		assertKey(inputs.Const("fixed"), "fixed")
		assertKey(inputs.Compose(inputs.Const("fixed"), inputs.Const("version")), "bfdbd353b64616a32e1cb1212e39f3639ada26124c8b18793ff157cca0627e00")
		const name = "ACCEPTANCE_MEMO_INPUT"
		t.Setenv(name, "")
		assertKey(inputs.Env(name), "memo:3a05f049b237fe1a2d819c92df37bc4d6cdb76286827f6eb9cd88dfd3263b968")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		assertKey(inputs.Env(name), "memo:d090f68a050417ac6b4aa108befb489e35b632bdd70985ba9d19e81342c87ba3")
	})
	t.Run("tree-symlink-isolation", testGoSDKMemoTreeIsolation)
}

func testGoSDKMemoTreeIsolation(t *testing.T) {
	recordLeaf(t)
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("fixture contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	const want = `"memo:b2ab38a1a470849d9d7adb9c43bf52bb7564d76a8ce8d8b77ae38c0fde60cf02"`
	property := func(contents []byte) bool {
		if err := os.WriteFile(filepath.Join(outside, "file.txt"), contents, 0o600); err != nil {
			t.Fatal(err)
		}
		raw, err := evalGoSDKMemo(t, inputs.Tree(root))
		return err == nil && string(raw) == want
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}
