package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func gitRepoWithProject(t *testing.T, withProject bool) string {
	t.Helper()
	repo := t.TempDir()
	runGitFixture(t, repo, "init", "--quiet", "-b", "main")
	runGitFixture(t, repo, "config", "commit.gpgsign", "false")
	runGitFixture(t, repo, "config", "core.hooksPath", filepath.Join(t.TempDir(), "no-hooks"))
	if withProject {
		if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), []byte("pipelines: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("no project\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "--quiet", "-m", "seed")
	return repo
}
func headCommit(t *testing.T, repo string) string {
	t.Helper()
	return runGitFixture(t, repo, "rev-parse", "HEAD")
}

func TestResolveRefCommitFindsABranchOnlyTheRemoteHas(t *testing.T) {
	origin := gitRepoWithProject(t, true)
	runGitFixture(t, origin, "branch", "feature")
	want := headCommit(t, origin)

	clone := t.TempDir()
	runGitFixture(t, filepath.Dir(clone), "clone", "--quiet", "--single-branch", "--branch", "main", origin, clone)
	runGitFixture(t, clone, "config", "commit.gpgsign", "false")

	rev, err := ResolveRefCommit(context.Background(), clone, "feature", nil)
	if err != nil {
		t.Fatalf("a branch the clone has not seen should resolve through the fetch: %v", err)
	}
	if rev != want {
		t.Errorf("rev = %s, want %s", rev, want)
	}
}
func TestResolveRefCommitRefusesADashLedRef(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	marker := filepath.Join(t.TempDir(), "executed")

	_, err := ResolveRefCommit(context.Background(), repo, "--upload-pack=touch "+marker, nil)
	if err == nil {
		t.Fatal("a ref beginning with a dash was accepted")
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatal("the ref reached git as an option and ran a command")
	}
}
