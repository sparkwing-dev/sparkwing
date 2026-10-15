package nativehost_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "rev-parse":
			root, err := os.Getwd()
			if err != nil {
				panic(err)
			}
			_, _ = os.Stdout.WriteString(root + "\n")
			return
		case "ls-files":
			_, _ = os.Stdout.WriteString("entry\x00")
			return
		}
	}
	os.Exit(m.Run())
}

func TestSharedMemoEntryVectors(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/helper-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Memo []struct {
			Kind    string `json:"kind"`
			Content string `json:"content"`
			Key     string `json:"key"`
			TreeKey string `json:"treeKey"`
		} `json:"memo"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), binary, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, vector := range vectors.Memo {
		t.Run(vector.Kind+"/"+vector.Content, func(t *testing.T) {
			entry := filepath.Join(root, "entry")
			var err error
			if vector.Kind == "file" {
				err = os.WriteFile(entry, []byte(vector.Content), 0o644)
			} else {
				err = os.Symlink(vector.Content, entry)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, resolve := range []sparkwing.MemoKeyFn{inputs.Files("entry"), inputs.RepoFiles()} {
				key, err := resolve(t.Context())
				if err != nil || string(key) != vector.Key {
					t.Fatalf("memo = %q, %v; want %q", key, err, vector.Key)
				}
			}
			if vector.TreeKey != "" {
				key, err := inputs.Tree(root)(t.Context())
				if err != nil || string(key) != vector.TreeKey {
					t.Fatalf("tree = %q, %v; want %q", key, err, vector.TreeKey)
				}
			}
			if err := os.Remove(entry); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSharedNestedGenerationGroups(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/helper-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		Job    string   `json:"job"`
		Groups []string `json:"groups"`
	}
	var vectors struct {
		Groups []entry `json:"groups"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", func(context.Context) error { return nil })
	sparkwing.GroupJobs(plan, "display", source)
	source.Generate(func(_ context.Context, generated *sparkwing.Plan) error {
		sparkwing.Job(generated, "child", func(context.Context) error { return nil }).Generate(func(_ context.Context, nested *sparkwing.Plan) error {
			sparkwing.Job(nested, "grandchild", func(context.Context) error { return nil })
			return nil
		})
		return nil
	})
	for _, job := range []string{"source", "child"} {
		result, err := sdkserve.DefaultOperations.Eval(t.Context(), plan, nodeproto.Request{Job: job, Closure: job + "/generate/0", Input: json.RawMessage(`null`)})
		if err != nil || result.Error != nil {
			t.Fatalf("generate %s: %v, %v", job, err, result.Error)
		}
	}
	var got []entry
	for _, job := range sdkserve.DefaultOperations.Document(plan).Jobs {
		slices.Sort(job.Groups)
		got = append(got, entry{Job: job.ID, Groups: job.Groups})
	}
	if !reflect.DeepEqual(got, vectors.Groups) {
		t.Fatalf("groups = %+v, want %+v", got, vectors.Groups)
	}
}
