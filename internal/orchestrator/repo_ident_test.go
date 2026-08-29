package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
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

func TestCurrentProfileKey_SurvivesABranchChange(t *testing.T) {
	repo, first := linkedWorktree(t, "sample-repo", "first-branch")
	_, second := linkedWorktree(t, "sample-repo", "second-branch")
	previousWorkDir := sparkwing.CurrentRuntime().WorkDir
	t.Cleanup(func() { sparkwing.SetWorkDir(previousWorkDir) })

	sparkwing.SetWorkDir(repo)
	t.Chdir(repo)
	want := currentProfileKey("pre-commit")
	if want != "11:sample-repopre-commit" {
		t.Fatalf("main checkout keyed %q, want 11:sample-repopre-commit", want)
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

	if got := currentProfileKey("build"); got != "11:sample-repobuild" {
		t.Fatalf("profile key after cwd change = %q, want 11:sample-repobuild", got)
	}
}

func TestCurrentProfileKey_FallsBackToCWDWithoutRunWorkDir(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "fallback-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	previousWorkDir := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir("")
	t.Cleanup(func() { sparkwing.SetWorkDir(previousWorkDir) })
	t.Chdir(repo)

	if got := currentProfileKey("build"); got != "13:fallback-repobuild" {
		t.Fatalf("profile key without run directory = %q, want 13:fallback-repobuild", got)
	}
}

func TestRepoShortName_BareRepoWorktreeResolvesToTheRepoName(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, "sample-repo.git", "worktrees", "feature-branch")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "linked-worktree")
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

func TestScopedProfileKey_SeparatesReposAndKeepsBareNameOutsideOne(t *testing.T) {
	if a, b := scopedProfileKey("alpha", "ci"), scopedProfileKey("beta", "ci"); a == b {
		t.Errorf("scopedProfileKey pooled %q and %q onto one key %q", "alpha/ci", "beta/ci", a)
	}
	if got := scopedProfileKey("alpha", "ci"); got != "5:alphaci" {
		t.Errorf("scopedProfileKey = %q, want 5:alphaci", got)
	}
	if got := scopedProfileKey("", "ci"); got != "ci" {
		t.Errorf("outside a repo: got %q, want the bare pipeline name", got)
	}
	if got := scopedProfileKey("alpha", ""); got != "" {
		t.Errorf("empty pipeline: got %q, want empty", got)
	}
}

func TestScopedProfileKey_SeparatesSlashBearingComponents(t *testing.T) {
	a := scopedProfileKey("github.com/example/acme-service", "build")
	b := scopedProfileKey("github.com/example", "acme-service/build")
	if a == b {
		t.Fatalf("distinct repository and pipeline components collapsed onto %q", a)
	}
}

func cloneWithOrigin(t *testing.T, dirName, originURL string) string {
	t.Helper()
	root := t.TempDir()
	clone := filepath.Join(root, dirName)
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = " + originURL + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if err := os.WriteFile(filepath.Join(clone, ".git", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestRepoShortName_CloneIsNamedByOriginNotItsDirectory(t *testing.T) {
	cases := []struct {
		name      string
		dir       string
		originURL string
	}{
		{"ephemeral clone", "build-checkout-2602713005", "https://github.com/example/acme-service.git"},
		{"scp form", "build-checkout-991", "git@github.com:example/acme-service.git"},
		{"scp form without user", "build-checkout-992", "github.com:example/acme-service.git"},
		{"no dot-git suffix", "build-checkout-77", "https://github.com/example/acme-service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := cloneWithOrigin(t, tc.dir, tc.originURL)
			if got := repoShortName(clone); got != "github.com/example/acme-service" {
				t.Fatalf("got %q, want github.com/example/acme-service; a clone keyed by its directory mints a fresh capacity profile per checkout, so measurements never reach MinSamples", got)
			}
		})
	}
}

func TestRepoShortName_OriginsWithTheSameBasenameStaySeparate(t *testing.T) {
	first := cloneWithOrigin(t, "first", "https://github.com/example/acme-service.git")
	second := cloneWithOrigin(t, "second", "https://git.example.net/another/acme-service.git")
	if a, b := repoShortName(first), repoShortName(second); a == b {
		t.Fatalf("distinct origins collapsed onto %q", a)
	}
}

func TestRepoShortName_OriginUsesGitConfigSemantics(t *testing.T) {
	cases := []struct {
		name   string
		config string
		extra  string
	}{
		{"case-insensitive key", "[remote \"origin\"]\n\tURL = https://github.com/example/acme-service.git\n", ""},
		{"included config", "[include]\n\tpath = origin.inc\n", "[remote \"origin\"]\n\turl = https://github.com/example/acme-service.git\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := cloneWithOrigin(t, "checkout", "https://invalid.example/placeholder.git")
			gitDir := filepath.Join(clone, ".git")
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.extra != "" {
				if err := os.WriteFile(filepath.Join(gitDir, "origin.inc"), []byte(tc.extra), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := repoShortName(clone); got != "github.com/example/acme-service" {
				t.Fatalf("got %q, want github.com/example/acme-service", got)
			}
		})
	}
}

func TestRepoShortName_LocalOriginsAreStableAndPrivate(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "private", "acme-service.git")
	first := cloneWithOrigin(t, "first", origin)
	second := cloneWithOrigin(t, "second", "file://"+origin)
	a, b := repoShortName(first), repoShortName(second)
	if a != b {
		t.Fatalf("equivalent local origins differ: %q and %q", a, b)
	}
	if strings.Contains(a, origin) || !strings.HasPrefix(a, "local:") {
		t.Fatalf("local identity exposes its path: %q", a)
	}
}

func TestRepoShortName_CloneWithoutOriginKeepsItsDirectoryName(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "local-only")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repoShortName(repo); got != "local-only" {
		t.Fatalf("got %q, want local-only; a repo with no remote has no identity beyond its path", got)
	}
}

func cloneWithAlternates(t *testing.T, dirName, objectsDir string) string {
	t.Helper()
	root := t.TempDir()
	clone := filepath.Join(root, dirName)
	info := filepath.Join(clone, ".git", "objects", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".git", "config"), []byte("[core]\n\tbare = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(objectsDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return clone
}

func objectStoreWithOrigin(t *testing.T, bare bool) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "acme-service")
	gitDir := filepath.Join(repo, ".git")
	if bare {
		gitDir = repo + ".git"
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[remote \"origin\"]\n\turl = https://github.com/example/acme-service.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(gitDir, "objects")
}

func TestRepoShortName_ThinCloneIsNamedByItsSharedObjectStore(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		bare bool
	}{
		{"bare object store", "build-checkout-2602713005", true},
		{"non-bare object store", "build-checkout-44", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := cloneWithAlternates(t, tc.dir, objectStoreWithOrigin(t, tc.bare))
			if got := repoShortName(clone); got != "github.com/example/acme-service" {
				t.Fatalf("got %q, want github.com/example/acme-service; ephemeral clones with no remote use a shared object store, so origin alone leaves a run keyed by its throwaway directory", got)
			}
		})
	}
}

func TestRepoShortName_RelativeAlternateResolvesFromObjectDatabase(t *testing.T) {
	objectsDir := objectStoreWithOrigin(t, true)
	clone := cloneWithAlternates(t, "build-checkout", objectsDir)
	info := filepath.Join(clone, ".git", "objects", "info")
	relative, err := filepath.Rel(filepath.Join(clone, ".git", "objects"), objectsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(relative+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoShortName(clone); got != "github.com/example/acme-service" {
		t.Fatalf("got %q, want github.com/example/acme-service", got)
	}
}
