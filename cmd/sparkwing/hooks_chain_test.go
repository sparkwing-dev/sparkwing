package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

// chainFixture is a throwaway machine for the hooks install: a real git
// checkout, a global git config that sets core.hooksPath, the hook directory
// it points at, and a sparkwing shim on PATH that records the pipelines a
// hook asks for.
type chainFixture struct {
	root       string
	repo       string
	globalDir  string
	binDir     string
	ranFile    string
	sentinelOf func(name string) string
}

func newChainFixture(t *testing.T) *chainFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	f := &chainFixture{
		root:      root,
		repo:      filepath.Join(root, "proj"),
		globalDir: filepath.Join(root, "globalhooks"),
		binDir:    filepath.Join(root, "bin"),
		ranFile:   filepath.Join(root, "pipelines-run"),
	}
	f.sentinelOf = func(name string) string { return filepath.Join(root, "fired-"+name) }

	for _, dir := range []string{f.repo, f.globalDir, f.binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExec(t, filepath.Join(f.binDir, "sparkwing"),
		"#!/bin/sh\necho \"$@\" >> "+f.ranFile+"\nexit 0\n")
	for _, name := range []string{"prepare-commit-msg", "pre-commit"} {
		writeExec(t, filepath.Join(f.globalDir, name),
			"#!/bin/sh\n: > "+f.sentinelOf(name)+"\nexit 0\n")
	}
	writeRepoFile(t, filepath.Join(root, "gitconfig"),
		"[core]\n\thooksPath = "+f.globalDir+"\n")
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), `pipelines:
  - name: gate
    entrypoint: Gate
    on:
      pre_commit: {}
`)
	writeRepoFile(t, filepath.Join(f.repo, "README.md"), "hello\n")
	f.git(t, "init", "-b", "main")
	return f
}

// env is the environment every git and hook invocation runs under. It is
// built from scratch rather than inherited: a test suite started by a git
// hook carries GIT_DIR and GIT_INDEX_FILE pointing at the outer repository,
// and a child git that inherits them commits into that repository instead of
// this fixture's.
func (f *chainFixture) env() []string {
	return []string{
		"PATH=" + f.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + f.root,
		"GIT_CONFIG_GLOBAL=" + filepath.Join(f.root, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=sparkwing test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=sparkwing test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	}
}

// asProcessEnv points this process's own environment at the fixture, so code
// that shells git itself -- the command entry points, which reach for the
// package-level runGit -- runs against the fixture's machine rather than the
// developer's. The repository-binding GIT_* variables are cleared with it: a
// suite started from a git hook inherits them and a child git would otherwise
// operate on the outer repository.
func (f *chainFixture) asProcessEnv(t *testing.T) {
	t.Helper()
	for _, kv := range f.env() {
		name, value, _ := strings.Cut(kv, "=")
		t.Setenv(name, value)
	}
	for _, name := range []string{"GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CONFIG_COUNT"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

func (f *chainFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := f.tryGit(f.repo, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (f *chainFixture) tryGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if dir == "" {
		cmd.Dir = f.repo
	}
	cmd.Env = f.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *chainFixture) ranPipelines(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.ranFile)
	if err != nil {
		return ""
	}
	return string(data)
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestHooksInstall_GateAndGlobalHookBothFireUnderAGlobalHooksPath(t *testing.T) {
	f := newChainFixture(t)

	captureStdout(t, func() {
		if err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing")); err != nil {
			t.Fatalf("install: %v", err)
		}
	})

	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "first")

	if ran := f.ranPipelines(t); !strings.Contains(ran, "run gate") {
		t.Errorf("the pre-commit gate never ran under a global core.hooksPath; sparkwing was asked for: %q", ran)
	}
	if _, err := os.Stat(f.sentinelOf("pre-commit")); err != nil {
		t.Error("the global pre-commit hook was dropped instead of chained after the gate")
	}
	if _, err := os.Stat(f.sentinelOf("prepare-commit-msg")); err != nil {
		t.Error("the global prepare-commit-msg hook stopped firing once the repo claimed core.hooksPath")
	}
}

func TestHooksInstall_KeepsTheGlobalHookFiringWhenItCannotForwardIt(t *testing.T) {
	f := newChainFixture(t)
	hooksDir, err := githooks.Dir(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(hooksDir, "prepare-commit-msg"),
		"#!/bin/sh\n: > "+f.sentinelOf("repo-prepare")+"\nexit 0\n")

	out := captureStdout(t, func() {
		if err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing")); err != nil {
			t.Fatalf("install: %v", err)
		}
	})

	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "first")

	if _, err := os.Stat(f.sentinelOf("prepare-commit-msg")); err != nil {
		t.Error("the machine's global prepare-commit-msg stopped firing after an install that could not forward it")
	}
	if !strings.Contains(out, "core.hooksPath left alone") || !strings.Contains(out, "prepare-commit-msg") {
		t.Errorf("install should say which global hook held the claim back:\n%s", out)
	}
}

func TestHooksCommands_InstallStatusAndUninstallDriveTheWholeChain(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	hooksDir := filepath.Join(f.repo, ".git", "hooks")

	out := captureStdout(t, func() {
		if err := runHooksInstall([]string{"--repo", f.repo}); err != nil {
			t.Fatalf("hooks install: %v", err)
		}
	})
	if !strings.Contains(out, "installed pre-commit -> gate, then the global hook") {
		t.Errorf("install should chain the gate ahead of the global hook:\n%s", out)
	}
	if got := strings.TrimSpace(f.git(t, "config", "--local", "core.hooksPath")); got != hooksDir {
		t.Errorf("core.hooksPath = %q, want the repo's own hook directory %s", got, hooksDir)
	}

	out = captureStdout(t, func() {
		if err := runHooksStatus([]string{"--repo", f.repo}); err != nil {
			t.Fatalf("hooks status: %v", err)
		}
	})
	if !strings.Contains(out, "pre-commit -> gate, then the global hook") ||
		!strings.Contains(out, "prepare-commit-msg -> the global hook") {
		t.Errorf("status should report the installed chain:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runHooksUninstall([]string{"--repo", f.repo}); err != nil {
			t.Fatalf("hooks uninstall: %v", err)
		}
	})
	if !strings.Contains(out, "removed pre-commit") {
		t.Errorf("uninstall should report what it removed:\n%s", out)
	}
	if got, err := f.tryGit(f.repo, "config", "--local", "core.hooksPath"); err == nil {
		t.Errorf("uninstall left the machine's hooks stranded behind core.hooksPath = %q", strings.TrimSpace(got))
	}
}

func TestHooksCommands_RejectARepoWithNoSparkwingDirectory(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	bare := filepath.Join(f.root, "elsewhere")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func([]string) error{
		"install":   runHooksInstall,
		"uninstall": runHooksUninstall,
		"status":    runHooksStatus,
	} {
		t.Run(name, func(t *testing.T) {
			err := run([]string{"--repo", bare})
			if err == nil {
				t.Fatalf("hooks %s accepted a directory that is not a sparkwing project", name)
			}
			if !strings.Contains(err.Error(), ".sparkwing") {
				t.Errorf("hooks %s should say what is missing: %v", name, err)
			}
		})
	}
}

func TestHooksInstall_FailingGateAbortsTheCommit(t *testing.T) {
	f := newChainFixture(t)
	writeExec(t, filepath.Join(f.binDir, "sparkwing"),
		"#!/bin/sh\necho \"$@\" >> "+f.ranFile+"\nexit 1\n")

	captureStdout(t, func() {
		if err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing")); err != nil {
			t.Fatalf("install: %v", err)
		}
	})

	f.git(t, "add", "-A")
	if out, err := f.tryGit(f.repo, "commit", "-m", "first"); err == nil {
		t.Fatalf("a failing gate let the commit through:\n%s", out)
	}
	if _, err := os.Stat(f.sentinelOf("pre-commit")); err == nil {
		t.Error("the chain ran the global hook after the gate already failed")
	}
}

func TestHooksInstall_DoctorStopsReportingAShadowedGate(t *testing.T) {
	f := newChainFixture(t)

	hooksDir, err := githooks.Dir(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(hooksDir, "pre-commit"), renderHookScript("pre-commit", []string{"gate"}, false))

	shadow, err := githooks.Detect(f.tryGit, f.repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if shadow == nil {
		t.Fatal("doctor missed a gate shadowed by the global core.hooksPath")
	}
	if shadow.Scope != "global" || len(shadow.Gates) != 1 || shadow.Gates[0] != "pre-commit" {
		t.Fatalf("doctor named the wrong finding: %+v", shadow)
	}

	captureStdout(t, func() {
		if err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing")); err != nil {
			t.Fatalf("install: %v", err)
		}
	})
	after, err := githooks.Detect(f.tryGit, f.repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if after != nil {
		t.Errorf("doctor still reports a shadowed gate after the install fixed it: %+v", after)
	}
}
