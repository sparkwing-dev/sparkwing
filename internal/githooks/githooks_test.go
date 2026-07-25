package githooks_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

// stubGit answers core.hooksPath lookups from fixed local and global values.
// An empty value stands for "not configured", which real git reports by
// exiting non-zero.
func stubGit(local, global string) githooks.Git {
	return func(_ string, args ...string) (string, error) {
		scope := ""
		if len(args) > 1 {
			scope = args[1]
		}
		switch scope {
		case "--local":
			if local == "" {
				return "", errors.New("core.hooksPath unset")
			}
			return local + "\n", nil
		case "--global":
			if global == "" {
				return "", errors.New("core.hooksPath unset")
			}
			return global + "\n", nil
		}
		return "", errors.New("unexpected git invocation")
	}
}

// repoWithGate builds a checkout whose hook directory holds one managed gate.
func repoWithGate(t *testing.T, hookName string) (repoRoot, hooksDir string) {
	t.Helper()
	repoRoot = t.TempDir()
	hooksDir = filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n# " + githooks.Marker + "\nsparkwing run gate\n"
	if err := os.WriteFile(filepath.Join(hooksDir, hookName), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return repoRoot, hooksDir
}

func TestDir_PlainCheckoutUsesItsOwnGitDirectory(t *testing.T) {
	repo, want := repoWithGate(t, "pre-commit")
	got, err := githooks.Dir(repo)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != want {
		t.Errorf("Dir = %s, want %s", got, want)
	}
}

func TestDir_LinkedWorktreeResolvesToTheMainCheckout(t *testing.T) {
	root := t.TempDir()
	mainGit := filepath.Join(root, "main", ".git")
	wtGit := filepath.Join(mainGit, "worktrees", "feature")
	if err := os.MkdirAll(wtGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGit, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+wtGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := githooks.Dir(worktree)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	want := filepath.Join(mainGit, "hooks")
	if got != want {
		t.Errorf("Dir = %s, want the main checkout's hooks dir %s", got, want)
	}
}

func TestDir_RejectsADirectoryThatIsNotACheckout(t *testing.T) {
	if _, err := githooks.Dir(t.TempDir()); err == nil {
		t.Fatal("Dir accepted a directory with no .git")
	}
}

func TestGates_ListsOnlyManagedHooksThatRunPipelines(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("pre-commit", "#!/bin/sh\n# "+githooks.Marker+"\nsparkwing run gate\n")
	write("prepare-commit-msg", "#!/bin/sh\n# "+githooks.Marker+"\nexec \"$hook\" \"$@\"\n")
	write("pre-push", "#!/bin/sh\necho hand written\n")

	got := githooks.Gates(dir)
	if len(got) != 1 || got[0] != "pre-commit" {
		t.Errorf("Gates = %v, want only the managed hook that runs a pipeline", got)
	}
}

func TestDetect_QuietWhenGitReadsTheHooksDirectory(t *testing.T) {
	repo, _ := repoWithGate(t, "pre-commit")
	shadow, err := githooks.Detect(stubGit("", ""), repo)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if shadow != nil {
		t.Errorf("Detect flagged a repo with no hooks path override: %+v", shadow)
	}
}

func TestDetect_QuietWhenTheOverridePointsAtTheHooksDirectory(t *testing.T) {
	repo, hooksDir := repoWithGate(t, "pre-commit")
	shadow, err := githooks.Detect(stubGit("", hooksDir), repo)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if shadow != nil {
		t.Errorf("Detect flagged an override that resolves to the hooks dir: %+v", shadow)
	}
}

func TestDetect_QuietWhenNoGateIsInstalled(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	shadow, err := githooks.Detect(stubGit("", "/etc/git-hooks"), repo)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if shadow != nil {
		t.Errorf("Detect flagged a repo with nothing installed: %+v", shadow)
	}
}

func TestDetect_ReportsAGlobalOverrideShadowingTheGate(t *testing.T) {
	repo, hooksDir := repoWithGate(t, "pre-push")
	shadow, err := githooks.Detect(stubGit("", "/home/dev/.config/git/hooks"), repo)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if shadow == nil {
		t.Fatal("Detect missed a global core.hooksPath shadowing an installed gate")
	}
	if shadow.Scope != "global" {
		t.Errorf("Scope = %q, want global", shadow.Scope)
	}
	if shadow.HooksDir != hooksDir || shadow.ActiveDir != "/home/dev/.config/git/hooks" {
		t.Errorf("Detect named the wrong directories: %+v", shadow)
	}
	if len(shadow.Gates) != 1 || shadow.Gates[0] != "pre-push" {
		t.Errorf("Gates = %v, want the installed pre-push gate", shadow.Gates)
	}
	if !strings.Contains(shadow.Summary(), "never fire") || !strings.Contains(shadow.Summary(), "pre-push") {
		t.Errorf("Summary does not say what stopped firing: %s", shadow.Summary())
	}
	if !strings.Contains(shadow.Remedy(), "sparkwing pipeline hooks install") {
		t.Errorf("Remedy does not point at the install that chains the two: %s", shadow.Remedy())
	}
}

func TestDetect_PrefersTheRepositoryOverrideOverTheMachineOne(t *testing.T) {
	repo, _ := repoWithGate(t, "pre-commit")
	shadow, err := githooks.Detect(stubGit("/repo/.githooks", "/home/dev/.config/git/hooks"), repo)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if shadow == nil {
		t.Fatal("Detect missed a local core.hooksPath shadowing an installed gate")
	}
	if shadow.Scope != "local" || shadow.ActiveDir != "/repo/.githooks" {
		t.Errorf("Detect did not honor git's local-wins precedence: %+v", shadow)
	}
	if !strings.Contains(shadow.Remedy(), "--unset core.hooksPath") {
		t.Errorf("Remedy for a local override should name the unset command: %s", shadow.Remedy())
	}
}
