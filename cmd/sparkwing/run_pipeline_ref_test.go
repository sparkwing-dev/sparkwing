package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func pipelineRefRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	mark := func(word string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte("pipelines: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "which"), []byte(word), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	mark("pipeline-main")
	git("add", ".")
	git("commit", "-q", "-m", "main")
	git("checkout", "-q", "-b", "feature")
	mark("pipeline-feature")
	git("commit", "-q", "-am", "change pipeline")
	return dir
}

func TestPipelineRefResolutionDoesNotBuildTheCallerPipeline(t *testing.T) {
	repo := pipelineRefRepo(t)
	t.Chdir(repo)
	got, _, err := resolveSubmitRepo(t.Context(), "build", "main")
	if err != nil || filepath.Clean(got) != filepath.Clean(repo) {
		t.Fatalf("resolve checkout without pipeline source = %q, %v; want %q", got, err, repo)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("not valid Go"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err = resolveSubmitRepo(t.Context(), "build", "main")
	if err != nil || filepath.Clean(got) != filepath.Clean(repo) {
		t.Fatalf("resolve checkout without a buildable pipeline = %q, %v; want %q", got, err, repo)
	}
}

func TestPipelineRefRequiresExecutionProjectConfiguration(t *testing.T) {
	repo := pipelineRefRepo(t)
	t.Chdir(repo)
	if err := os.Remove(filepath.Join(repo, ".sparkwing", "sparkwing.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveSubmitRepo(t.Context(), "build", "main"); err == nil {
		t.Fatal("submission accepted an execution checkout without project configuration")
	}
}

func pipelineRefStore(t *testing.T) (orchestrator.Paths, *store.Store) {
	t.Helper()
	paths := orchestrator.PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return paths, st
}

func TestPipelineRefFlagAllowsForegroundRun(t *testing.T) {
	for _, args := range [][]string{{"--sw-pipeline-ref", "main"}, {"--sw-pipeline-ref=main"}} {
		flags, rest := parseRunFlags(args)
		if flags.pipelineRef != "main" || len(rest) != 0 {
			t.Errorf("parse %v = %q with %v left over, want main and nothing left", args, flags.pipelineRef, rest)
		}
		if err := refuseDetachedOnlyFlags(flags); err != nil {
			t.Errorf("a foreground run with %v = %v", args, err)
		}
	}
}
