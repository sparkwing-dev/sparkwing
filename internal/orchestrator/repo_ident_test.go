package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRepoShortName_FindsGitToplevelBasename(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "myproject")
	nested := filepath.Join(repo, "sub", "deep")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := repoShortName(repo); got != "myproject" {
		t.Errorf("at toplevel: got %q, want myproject", got)
	}
	if got := repoShortName(nested); got != "myproject" {
		t.Errorf("nested: got %q, want myproject", got)
	}
}

func TestRepoShortName_GitFileMarksLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "linked-wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoShortName(wt); got != "linked-wt" {
		t.Errorf("worktree .git file: got %q, want linked-wt", got)
	}
}

// linkedWorktree builds a repo checkout and a linked worktree of it laid out
// the way git does: the worktree is a plain directory whose .git is a file
// pointing at <repo>/.git/worktrees/<name>. It returns both directories.
func linkedWorktree(t *testing.T, repoName, worktreeName string) (repo, worktree string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, repoName)
	gitDir := filepath.Join(repo, ".git", "worktrees", worktreeName)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree = filepath.Join(root, "worktrees", worktreeName)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, worktree
}

// TestRepoShortName_LinkedWorktreeResolvesToItsRepo keeps capacity learning
// shared by every worktree of one repository.
func TestRepoShortName_LinkedWorktreeResolvesToItsRepo(t *testing.T) {
	repo, worktree := linkedWorktree(t, "sample-repo", "feature-branch")

	if got := repoShortName(repo); got != "sample-repo" {
		t.Errorf("main checkout: got %q, want sample-repo", got)
	}
	if got := repoShortName(worktree); got != "sample-repo" {
		t.Errorf("linked worktree: got %q, want sample-repo", got)
	}
	nested := filepath.Join(worktree, "backend", "cmd")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repoShortName(nested); got != "sample-repo" {
		t.Errorf("inside a linked worktree: got %q, want sample-repo", got)
	}
}

// TestCurrentProfileKey_SurvivesABranchChange states the acceptance criterion
// directly: the same pipeline in two worktrees of one repo reads and writes
// one profile row, so a gate arrives already knowing what it costs.
func TestCurrentProfileKey_SurvivesABranchChange(t *testing.T) {
	repo, first := linkedWorktree(t, "sample-repo", "first-branch")
	_, second := linkedWorktree(t, "sample-repo", "second-branch")
	previousWorkDir := sparkwing.CurrentRuntime().WorkDir
	t.Cleanup(func() { sparkwing.SetWorkDir(previousWorkDir) })

	sparkwing.SetWorkDir(repo)
	t.Chdir(repo)
	want := currentProfileKey("pre-commit")
	if want != "sample-repo/pre-commit" {
		t.Fatalf("main checkout keyed %q, want sample-repo/pre-commit", want)
	}
	sparkwing.SetWorkDir(first)
	t.Chdir(first)
	if got := currentProfileKey("pre-commit"); got != want {
		t.Errorf("first worktree keyed %q, want %q", got, want)
	}
	sparkwing.SetWorkDir(second)
	t.Chdir(second)
	if got := currentProfileKey("pre-commit"); got != want {
		t.Errorf("second worktree keyed %q, want %q", got, want)
	}
}

func TestCurrentProfileKey_UsesRunWorkDirAfterCWDChanges(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "sample-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	previousWorkDir := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(repo)
	t.Cleanup(func() { sparkwing.SetWorkDir(previousWorkDir) })
	t.Chdir(t.TempDir())

	if got := currentProfileKey("build"); got != "sample-repo/build" {
		t.Fatalf("profile key after cwd change = %q, want sample-repo/build", got)
	}
}

// TestRepoShortName_BareRepoWorktreeResolvesToTheRepoName covers the pointer
// shape a worktree of a bare repo carries: <repo>.git/worktrees/<name>, with
// no .git directory level to strip.
func TestRepoShortName_BareRepoWorktreeResolvesToTheRepoName(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, "sample-repo.git", "worktrees", "feature-branch")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "bw-1459")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoShortName(wt); got != "sample-repo" {
		t.Errorf("bare-repo worktree: got %q, want sample-repo", got)
	}
}

// TestRepoShortName_SubmoduleKeepsItsOwnIdentity guards the other .git file
// shape. A submodule's pointer names <super>/.git/modules/<name> and has no
// worktrees segment, so the submodule stays its own repo for pricing rather
// than pooling its costs into its superproject.
func TestRepoShortName_SubmoduleKeepsItsOwnIdentity(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "super", "vendor", "lib")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "super", ".git", "modules", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	pointer := "gitdir: " + filepath.Join(root, "super", ".git", "modules", "lib") + "\n"
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoShortName(sub); got != "lib" {
		t.Errorf("submodule: got %q, want lib", got)
	}
}

func TestRepoShortName_EmptyOutsideAnyRepo(t *testing.T) {
	if got := repoShortName(t.TempDir()); got != "" {
		t.Errorf("outside a repo: got %q, want empty", got)
	}
}

// TestScopedProfileKey_SeparatesReposAndKeepsBareNameOutsideOne keeps
// identically named pipelines in separate repositories from sharing
// capacity-profile rows.
func TestScopedProfileKey_SeparatesReposAndKeepsBareNameOutsideOne(t *testing.T) {
	if a, b := scopedProfileKey("alpha", "ci"), scopedProfileKey("beta", "ci"); a == b {
		t.Errorf("scopedProfileKey pooled %q and %q onto one key %q", "alpha/ci", "beta/ci", a)
	}
	if got := scopedProfileKey("alpha", "ci"); got != "alpha/ci" {
		t.Errorf("scopedProfileKey = %q, want alpha/ci", got)
	}
	if got := scopedProfileKey("", "ci"); got != "ci" {
		t.Errorf("outside a repo: got %q, want the bare pipeline name", got)
	}
	if got := scopedProfileKey("alpha", ""); got != "" {
		t.Errorf("empty pipeline: got %q, want empty", got)
	}
}
