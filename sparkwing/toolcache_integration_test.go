package sparkwing_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Two worktrees of one repo hold the same module path, the same
// relative file path, and identical bytes -- only the absolute prefix
// differs. That is the input golangci-lint's content-keyed cache
// cannot tell apart: sharing one cache between them makes the second
// run report the first worktree's file path (and, once that worktree
// is deleted, a path that no longer exists). Each run gets its own
// ToolCacheDir here, so each must report the tree it actually linted.
func TestToolCacheDir_LintReportsTheWorktreeItRanIn(t *testing.T) {
	if testing.Short() {
		t.Skip("runs golangci-lint twice")
	}
	for _, bin := range []string{"golangci-lint", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}

	root := t.TempDir()
	first := seedLintWorktree(t, filepath.Join(root, "wt-first"))
	second := seedLintWorktree(t, filepath.Join(root, "wt-second"))

	firstOut := lintWithScopedCache(t, first)
	if !strings.Contains(firstOut, first) {
		t.Fatalf("lint in %q did not report its own tree:\n%s", first, firstOut)
	}

	secondOut := lintWithScopedCache(t, second)
	if strings.Contains(secondOut, first) {
		t.Fatalf("lint in %q reported the sibling worktree %q:\n%s", second, first, secondOut)
	}
	if !strings.Contains(secondOut, second) {
		t.Fatalf("lint in %q did not report its own tree:\n%s", second, secondOut)
	}
}

// seedLintWorktree writes a minimal module carrying one finding the
// default linter set reports, and returns the worktree root.
func seedLintWorktree(t *testing.T, dir string) string {
	t.Helper()
	pkgDir := filepath.Join(dir, "pkg", "sample")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("seed %s: %v", dir, err)
	}
	files := map[string]string{
		filepath.Join(dir, "go.mod"):       "module example.com/wing\n\ngo 1.23\n",
		filepath.Join(pkgDir, "unused.go"): "package sample\n\nfunc unusedHelper() int { return 1 }\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	return dir
}

// lintWithScopedCache runs golangci-lint in dir with the cache
// ToolCacheDir hands that worktree, the way a gate's lint step does,
// and returns the combined output. Findings are expected, so a
// non-zero exit is not a test failure. --no-config pins the default
// linter set regardless of what sits above the temp dir, and
// --path-mode abs makes the reported location unambiguous. TMPDIR is
// private because golangci-lint's parallel-runner lock is one file per
// temp directory: on the shared default this run is refused by any
// gate linting alongside it, and the refusal reads as a test failure.
func lintWithScopedCache(t *testing.T, dir string) string {
	t.Helper()
	useWorkDir(t, dir)

	res, err := sparkwing.Bash(context.Background(), "golangci-lint run --no-config --path-mode abs ./...").
		Dir(dir).
		Env("GOLANGCI_LINT_CACHE", toolCacheDir(t, "golangci-lint")).
		Env("TMPDIR", t.TempDir()).
		Capture()
	out := res.Stdout + res.Stderr
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) {
		out = execErr.Stdout + execErr.Stderr
	} else if err != nil {
		t.Fatalf("golangci-lint in %s: %v", dir, err)
	}
	if !strings.Contains(out, "unusedHelper") {
		t.Fatalf("golangci-lint in %s reported no finding:\n%s", dir, out)
	}
	return out
}
