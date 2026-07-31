package sparkwing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// seedLintTree writes a module whose only finding names symbol, so two
// fixtures can be told apart by what they report rather than only by
// where they are.
func seedLintTree(t *testing.T, dir, symbol string) string {
	t.Helper()
	pkgDir := filepath.Join(dir, "pkg", "sample")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("seed %s: %v", dir, err)
	}
	files := map[string]string{
		filepath.Join(dir, "go.mod"):       "module example.com/wing\n\ngo 1.23\n",
		filepath.Join(pkgDir, "unused.go"): "package sample\n\nfunc " + symbol + "() int { return 1 }\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	return dir
}

// lintThroughSlot runs golangci-lint the way a gate would, through the
// slot rather than in the worktree, and returns the combined output.
// Findings are expected, so a non-zero exit is not a failure.
// --path-mode abs makes the reported location unambiguous, and a
// private TMPDIR keeps golangci-lint's box-wide parallel-runner lock
// off the one the fleet's gates are using.
func lintThroughSlot(t *testing.T, slot *sparkwing.LintSlot) string {
	t.Helper()
	cmd := sparkwing.Bash(context.Background(), "golangci-lint run --no-config --path-mode abs ./...")
	res, err := slot.Configure(cmd, "GOLANGCI_LINT_CACHE").
		Env("TMPDIR", t.TempDir()).
		Capture()
	out := res.Stdout + res.Stderr
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) {
		return execErr.Stdout + execErr.Stderr
	}
	if err != nil {
		t.Fatalf("golangci-lint through %s: %v", slot.Path, err)
	}
	return out
}

// reportedFiles pulls the absolute .go paths out of a lint report.
func reportedFiles(out string) []string {
	var files []string
	for _, field := range strings.FieldsFunc(out, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '"'
	}) {
		path, _, found := strings.Cut(field, ".go:")
		if !found || !strings.HasPrefix(path, "/") {
			continue
		}
		files = append(files, path+".go")
	}
	return files
}

// The negative control this design exists to pass: two worktrees
// linting at the same time through canonical paths, each with a real
// finding of its own, and neither told about the other. A shared cache
// directory fails this -- measured on sparkwing, all 49 findings in the
// second worktree carried the first worktree's paths.
func TestLintSlots_ConcurrentWorktreesEachReportOnlyTheirOwnPaths(t *testing.T) {
	root := lintFixtureRoot(t)
	tool := lintSlotTool(t)

	alpha := seedLintTree(t, filepath.Join(root, "alpha"), "unusedAlpha")
	beta := seedLintTree(t, filepath.Join(root, "beta"), "unusedBeta")

	alphaSlot := acquireFor(t, tool, alpha)
	betaSlot := acquireFor(t, tool, beta)
	if !alphaSlot.Canonical || !betaSlot.Canonical {
		t.Skip("fewer than two lint slots available on this machine")
	}

	var wg sync.WaitGroup
	var alphaOut, betaOut string
	wg.Add(2)
	go func() { defer wg.Done(); alphaOut = lintThroughSlot(t, alphaSlot) }()
	go func() { defer wg.Done(); betaOut = lintThroughSlot(t, betaSlot) }()
	wg.Wait()

	if !strings.Contains(alphaOut, "unusedAlpha") {
		t.Fatalf("alpha reported no finding of its own:\n%s", alphaOut)
	}
	if !strings.Contains(betaOut, "unusedBeta") {
		t.Fatalf("beta reported no finding of its own:\n%s", betaOut)
	}
	if strings.Contains(alphaOut, "unusedBeta") || strings.Contains(alphaOut, betaSlot.Path) {
		t.Fatalf("alpha was told about beta's tree:\n%s", alphaOut)
	}
	if strings.Contains(betaOut, "unusedAlpha") || strings.Contains(betaOut, alphaSlot.Path) {
		t.Fatalf("beta was told about alpha's tree:\n%s", betaOut)
	}
	for _, out := range []string{alphaOut, betaOut} {
		for _, f := range reportedFiles(out) {
			if _, err := os.Stat(f); err != nil {
				t.Fatalf("reported a file the reader cannot open: %s", f)
			}
		}
	}
}

// The failure a per-worktree cache exists to prevent, reached the way
// the fleet reaches it: a worktree lands, is deleted, and the next
// ticket's worktree inherits its warm cache. Through a slot the stored
// path is the slot's, so it follows the new holder instead of naming a
// tree that is gone.
func TestLintSlot_ReusedCacheNamesTheNewHolderNotTheDeletedDonor(t *testing.T) {
	root := lintFixtureRoot(t)
	tool := lintSlotTool(t)

	donor := seedLintTree(t, filepath.Join(root, "donor"), "unusedHelper")
	target := seedLintTree(t, filepath.Join(root, "target"), "unusedHelper")

	donorSlot := acquireFor(t, tool, donor)
	if !donorSlot.Canonical {
		t.Skip("no lint slot available on this machine")
	}
	if out := lintThroughSlot(t, donorSlot); !strings.Contains(out, "unusedHelper") {
		t.Fatalf("donor lint reported no finding, so nothing was cached:\n%s", out)
	}
	donorSlot.Release()

	if err := os.RemoveAll(donor); err != nil {
		t.Fatalf("remove donor: %v", err)
	}

	targetSlot := acquireFor(t, tool, target)
	if targetSlot.Path != donorSlot.Path {
		t.Fatalf("target got slot %q, not the donor's %q, so this is not the reuse case",
			targetSlot.Path, donorSlot.Path)
	}
	out := lintThroughSlot(t, targetSlot)

	if !strings.Contains(out, "unusedHelper") {
		t.Fatalf("the inherited cache lost the finding:\n%s", out)
	}
	if strings.Contains(out, donor) {
		t.Fatalf("the inherited cache named the deleted donor tree:\n%s", out)
	}
	files := reportedFiles(out)
	if len(files) == 0 {
		t.Fatalf("no absolute path in the report, so this asserts nothing:\n%s", out)
	}
	for _, f := range files {
		real, err := filepath.EvalSymlinks(f)
		if err != nil {
			t.Fatalf("reported a file the reader cannot open: %s", f)
		}
		if !strings.HasPrefix(real, resolves(t, target)) {
			t.Fatalf("reported %s, which resolves to %s -- outside the holder's tree", f, real)
		}
	}
}

// A cache that makes lint fast by not looking is worse than a slow
// lint. The finding planted here is new to the tree that inherited the
// warm cache, so only a run that actually analyzed the changed package
// can report it.
func TestLintSlot_WarmCacheStillCatchesANewViolation(t *testing.T) {
	root := lintFixtureRoot(t)
	tool := lintSlotTool(t)

	donor := seedLintTree(t, filepath.Join(root, "donor"), "unusedHelper")
	target := seedLintTree(t, filepath.Join(root, "target"), "unusedHelper")

	donorSlot := acquireFor(t, tool, donor)
	if !donorSlot.Canonical {
		t.Skip("no lint slot available on this machine")
	}
	lintThroughSlot(t, donorSlot)
	donorSlot.Release()

	planted := filepath.Join(target, "pkg", "sample", "planted.go")
	if err := os.WriteFile(planted, []byte("package sample\n\nfunc unusedPlanted() int { return 2 }\n"), 0o644); err != nil {
		t.Fatalf("plant violation: %v", err)
	}

	targetSlot := acquireFor(t, tool, target)
	if targetSlot.Cache != donorSlot.Cache {
		t.Fatalf("target got cache %q, not the donor's %q -- this ran cold, so it says "+
			"nothing about what a warm cache reports", targetSlot.Cache, donorSlot.Cache)
	}

	out := lintThroughSlot(t, targetSlot)
	if !strings.Contains(out, "unusedPlanted") {
		t.Fatalf("a warm slot cache hid a violation that is new to this tree:\n%s", out)
	}
}
