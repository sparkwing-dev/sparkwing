package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// safety: the baseline filter is what an alias path defeats, so a fixture
// without it cannot catch the defect.
const lintBaselineFixtureConfig = lintFixtureConfig + `
issues:
  new-from-merge-base: ` + gateBaselineRef + `
`

// TestLintReportsAFindingIntroducedInALinkedWorktree lints a git worktree
// holding a finding the baseline does not have.
func TestLintReportsAFindingIntroducedInALinkedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	requireGolangciLint(t)
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

	t.Chdir(worktree)
	t.Cleanup(func() { _ = os.RemoveAll(lintCacheDir()) })

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

// TestLintCacheLivesInTheWorktreesGitDir keeps the 80,000-file cache out of
// the tree the pipeline's source key walks, and apart from every other tree.
func TestLintCacheLivesInTheWorktreesGitDir(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	writeGoFile(t, filepath.Join(origin, "go.mod"), "module fixture\n\ngo 1.25\n")
	gitInit(t, origin)
	gitCommitAll(t, origin, "base")
	linked := filepath.Join(root, "linked")
	runTestGit(t, origin, "worktree", "add", "-b", "linked", linked, "HEAD")
	dirs := map[string]string{}
	for _, tree := range []string{origin, linked} {
		t.Chdir(tree)
		dir := lintCacheDir()
		if strings.HasPrefix(dir, tree+string(filepath.Separator)) && !strings.HasPrefix(dir, filepath.Join(tree, ".git")) {
			t.Fatalf("%s: lint cache %s sits in the walked tree", tree, dir)
		}
		dirs[tree] = dir
	}
	if dirs[origin] == dirs[linked] {
		t.Fatalf("two worktrees share the lint cache %s", dirs[origin])
	}
}
