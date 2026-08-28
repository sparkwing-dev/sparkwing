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

const lintSlotIntegrationPool = "8"

func lintSlotFixtureRoot(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("NOT VERIFIED under -short: that golangci-lint reports slot paths " +
			"rather than the worktree the symlink resolves to. The mechanism itself " +
			"(os.Getwd preferring $PWD) is still covered by " +
			"TestLintSlot_GetwdPrefersPWDOverTheResolvedPath, which needs no toolchain " +
			"and never skips. Run without -short for the end-to-end control.")
	}
	return lintFixtureRoot(t)
}

func requireCanonicalSlot(t *testing.T, slot *sparkwing.LintSlot, what string) {
	t.Helper()
	if !slot.Canonical {
		t.Fatalf("%s got the private-cache fallback (%+v) rather than a canonical slot. "+
			"With a private tool name and a pool of %s this cannot be contention, so "+
			"slot creation itself failed -- and this test is the only control over the "+
			"PWD mechanism, so it must not pass quietly", what, slot, lintSlotIntegrationPool)
	}
}

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

func TestLintSlots_ConcurrentWorktreesEachReportOnlyTheirOwnPaths(t *testing.T) {
	t.Setenv(sparkwing.LintSlotsEnv, lintSlotIntegrationPool)
	root := lintSlotFixtureRoot(t)
	tool := lintSlotTool(t)

	alpha := seedLintTree(t, filepath.Join(root, "alpha"), "unusedAlpha")
	beta := seedLintTree(t, filepath.Join(root, "beta"), "unusedBeta")

	alphaSlot := acquireFor(t, tool, alpha)
	betaSlot := acquireFor(t, tool, beta)
	requireCanonicalSlot(t, alphaSlot, "alpha")
	requireCanonicalSlot(t, betaSlot, "beta")

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

func TestLintSlot_ReusedCacheNamesTheNewHolderNotTheDeletedDonor(t *testing.T) {
	t.Setenv(sparkwing.LintSlotsEnv, lintSlotIntegrationPool)
	root := lintSlotFixtureRoot(t)
	tool := lintSlotTool(t)

	donor := seedLintTree(t, filepath.Join(root, "donor"), "unusedHelper")
	target := seedLintTree(t, filepath.Join(root, "target"), "unusedHelper")

	donorSlot := acquireFor(t, tool, donor)
	requireCanonicalSlot(t, donorSlot, "donor")
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

func TestLintSlot_WarmCacheStillCatchesANewViolation(t *testing.T) {
	t.Setenv(sparkwing.LintSlotsEnv, lintSlotIntegrationPool)
	root := lintSlotFixtureRoot(t)
	tool := lintSlotTool(t)

	donor := seedLintTree(t, filepath.Join(root, "donor"), "unusedHelper")
	target := seedLintTree(t, filepath.Join(root, "target"), "unusedHelper")

	donorSlot := acquireFor(t, tool, donor)
	requireCanonicalSlot(t, donorSlot, "donor")
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
