package githooks_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "gate@example.com")
	runGit(t, repo, "config", "user.name", "gate")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "seed.txt")

	runGit(t, repo, "-c", "core.hooksPath="+t.TempDir(), "commit", "-q", "-m", "seed")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func installGate(t *testing.T, hooksDir string, selfTest bool) string {
	t.Helper()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n# " + githooks.Marker + "\n"
	if selfTest {
		body += githooks.SelfTestScript()
	}
	body += "sparkwing run pre-commit\n"
	path := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var preCommit = []string{"pre-commit"}

func pointGitAt(t *testing.T, repo, hooksDir string) githooks.Git {
	t.Helper()
	runGit(t, repo, "config", "core.hooksPath", hooksDir)
	return stubGit(hooksDir, "")
}

func TestFire_RefusedWhenTheRepoRunsItsOwnGate(t *testing.T) {
	repo := gitRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	hook := installGate(t, hooks, true)
	before := runGit(t, repo, "rev-parse", "HEAD")

	got := githooks.Fire(pointGitAt(t, repo, hooks), repo, preCommit)
	if got.Verdict != githooks.FireRefused {
		t.Fatalf("Verdict = %s (%s), want %s", got.Verdict, got.Detail, githooks.FireRefused)
	}
	if !got.Enforced() {
		t.Error("Enforced() = false, want true")
	}
	if !githooks.SameDir(filepath.Dir(got.Hook), hooks) {
		t.Errorf("Hook = %s, want a file in %s (installed %s)", got.Hook, hooks, hook)
	}
	if got.HeadMoved {
		t.Error("HeadMoved = true, want false")
	}
	if after := runGit(t, repo, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved: %s -> %s", before, after)
	}
}

func TestFire_AcceptedWhenAHooksPathOverrideShadowsTheInstalledGate(t *testing.T) {
	repo := gitRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	installGate(t, hooks, true)
	elsewhere := t.TempDir()

	got := githooks.Fire(pointGitAt(t, repo, elsewhere), repo, preCommit)
	if got.Verdict != githooks.FireAccepted {
		t.Fatalf("Verdict = %s (%s), want %s", got.Verdict, got.Detail, githooks.FireAccepted)
	}
	if got.Enforced() {
		t.Error("Enforced() = true, want false: the gate is installed and never ran")
	}
}

func TestFire_BorrowedWhenTheGateThatRefusesLivesInAnotherRepo(t *testing.T) {
	repo := gitRepo(t)
	sibling := t.TempDir()
	siblingHooks := filepath.Join(sibling, "hooks")
	installGate(t, siblingHooks, true)

	got := githooks.Fire(pointGitAt(t, repo, siblingHooks), repo, preCommit)
	if got.Verdict != githooks.FireBorrowed {
		t.Fatalf("Verdict = %s (%s), want %s", got.Verdict, got.Detail, githooks.FireBorrowed)
	}
	if got.Enforced() {
		t.Error("Enforced() = true, want false: the gate that refused is another repo's")
	}
	if !strings.Contains(got.Detail, siblingHooks) {
		t.Errorf("Detail = %q, want it to name %s", got.Detail, siblingHooks)
	}
}

func TestFire_AcceptedWhenNoGateIsInstalledAtAll(t *testing.T) {
	repo := gitRepo(t)
	got := githooks.Fire(pointGitAt(t, repo, filepath.Join(repo, ".git", "hooks")), repo, preCommit)
	if got.Verdict != githooks.FireAccepted {
		t.Fatalf("Verdict = %s (%s), want %s", got.Verdict, got.Detail, githooks.FireAccepted)
	}
}

func TestFire_UnprovableWhenTheInstalledGatePredatesTheSelfTest(t *testing.T) {
	repo := gitRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	installGate(t, hooks, false)

	got := githooks.Fire(pointGitAt(t, repo, hooks), repo, preCommit)
	if got.Verdict != githooks.FireUnprovable {
		t.Fatalf("Verdict = %s (%s), want %s", got.Verdict, got.Detail, githooks.FireUnprovable)
	}
	if got.Enforced() {
		t.Error("Enforced() = true, want false: the question was not answered")
	}
	if !strings.Contains(got.Detail, "hooks install") {
		t.Errorf("Detail = %q, want the command that fixes it", got.Detail)
	}
}

func TestFire_UnprovableWithoutRunningAHookSparkwingDidNotWrite(t *testing.T) {
	repo := gitRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(t.TempDir(), "ran")
	body := "#!/bin/sh\n# mentions " + githooks.SelfTestEnv + " without being managed\ntouch " + witness + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	got := githooks.Fire(pointGitAt(t, repo, hooks), repo, preCommit)
	if got.Verdict != githooks.FireUnprovable {
		t.Fatalf("Verdict = %s (%s), want %s", got.Verdict, got.Detail, githooks.FireUnprovable)
	}
	if _, err := os.Stat(witness); err == nil {
		t.Error("the hand-written hook ran; a diagnostic must not execute one")
	}
}

func TestFire_UndeclaredWhenNoPipelineAsksForACommitGate(t *testing.T) {
	repo := gitRepo(t)
	got := githooks.Fire(stubGit("", ""), repo, []string{"post-commit"})
	if got.Verdict != githooks.FireUndeclared {
		t.Fatalf("Verdict = %s, want %s", got.Verdict, githooks.FireUndeclared)
	}
}

func TestFire_LeavesTheRepoUntouched(t *testing.T) {
	repo := gitRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	installGate(t, hooks, true)
	before := runGit(t, repo, "rev-parse", "HEAD")

	githooks.Fire(pointGitAt(t, repo, hooks), repo, preCommit)

	if after := runGit(t, repo, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved: %s -> %s", before, after)
	}
	if status := runGit(t, repo, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("working tree dirty after the attempt:\n%s", status)
	}
	if list := runGit(t, repo, "worktree", "list"); strings.Count(strings.TrimSpace(list), "\n") != 0 {
		t.Errorf("throwaway worktree left registered:\n%s", list)
	}
}

func TestSelfTestScript_OnlyEverRefuses(t *testing.T) {
	got := githooks.SelfTestScript()
	if !strings.Contains(got, "exit 1") {
		t.Errorf("script = %q, want it to refuse", got)
	}
	if strings.Contains(got, "exit 0") {
		t.Errorf("script = %q, want no path that lets a commit through", got)
	}
}

func TestCarriesSelfTest_TellsAGuardedHookFromAnUnguardedOne(t *testing.T) {
	if !githooks.CarriesSelfTest("#!/bin/sh\n" + githooks.SelfTestScript()) {
		t.Error("CarriesSelfTest = false for a script carrying the guard")
	}
	if githooks.CarriesSelfTest("#!/bin/sh\nsparkwing run pre-commit\n") {
		t.Error("CarriesSelfTest = true for a script without it")
	}
}
