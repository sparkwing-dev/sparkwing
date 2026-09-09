package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the baseline filter is what an alias path defeats, so a fixture
// without it cannot catch the defect.
const lintBaselineFixtureConfig = lintFixtureConfig + `
issues:
  new-from-merge-base: ` + gateBaselineRef + `
`

// TestLintReportsAFindingIntroducedInALinkedWorktree lints a git worktree
// holding a finding the baseline does not have.
//
// The gate reported "0 issues" for such a tree while hosted CI rejected the
// same commit. It lent the run a canonical alias path so worktrees could share
// one linter cache; git resolves the alias and reports the real worktree as the
// repository root, so every finding the linter recorded under the alias sat
// outside the diff and the baseline filter dropped it.
func TestLintReportsAFindingIntroducedInALinkedWorktree(t *testing.T) {
	requireGolangciLint(t)
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv("SPARKWING_CACHE_TOKEN", "")
	root := t.TempDir()

	origin := filepath.Join(root, "origin")
	writeGoFile(t, filepath.Join(origin, "go.mod"), "module fixture\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(origin, ".golangci.yml"), lintBaselineFixtureConfig)
	writeGoFile(t, filepath.Join(origin, "internal", "clean.go"),
		"package internal\n\nfunc Clean() int { return 1 }\n")
	gitInit(t, origin)
	gitCommitAll(t, origin, "clean base")
	runTestGit(t, origin, "update-ref", "refs/remotes/"+gateBaselineRef, "HEAD")

	worktree := filepath.Join(root, "linked")
	runTestGit(t, origin, "worktree", "add", "-b", "finding", worktree, "HEAD")

	previous := sparkwing.WorkDir()
	sparkwing.SetWorkDir(worktree)
	t.Cleanup(func() { sparkwing.SetWorkDir(previous) })
	t.Cleanup(func() { _ = os.RemoveAll(sparkwing.ToolCacheDir("golangci-lint")) })

	writeGoFile(t, filepath.Join(worktree, "internal", "negative_control.go"),
		ineffassignViolation("internal"))
	gitAddAll(t, worktree)

	err := runGolangciLint(context.Background())
	if err == nil {
		t.Fatal("lint passed a finding a linked worktree introduced after the baseline, " +
			"so the gate does not gate in a worktree")
	}
	if strings.Contains(err.Error(), "could not run") {
		t.Skipf("another golangci-lint held the box-wide lock, so nothing was learned: %v", err)
	}
	if !strings.Contains(err.Error(), "ineffectual assignment") {
		t.Fatalf("lint failed in a linked worktree for some reason other than the "+
			"planted finding: %v", err)
	}
}
