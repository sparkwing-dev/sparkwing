package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshScaffoldPlansAndExplainsInitialCommitBeforeRun(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the scaffold through the real SDK")
	}
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "state"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Chdir(repo)
	runSnapshotGit(t, repo, "init", "--quiet")
	var commandErr error
	captureStdout(t, func() {
		commandErr = runSparkwing([]string{"pipeline", "new", "--pipeline", "hello"})
	})
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.OpenFile(filepath.Join(repo, ".sparkwing", "go.mod"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := fmt.Fprintf(mod, "\nreplace github.com/sparkwing-dev/sparkwing => %q\n", root)
	if err := mod.Close(); err != nil || writeErr != nil {
		t.Fatalf("local SDK replacement: %v %v", writeErr, err)
	}
	output := captureStdout(t, func() {
		commandErr = runSparkwing([]string{"pipeline", "plan", "--pipeline", "hello", "-o=json"})
	})
	var plan planPreviewDoc
	if commandErr != nil || json.Unmarshal([]byte(output), &plan) != nil || len(plan.Jobs) != 1 {
		t.Fatalf("unborn scaffold plan: %v: %s", commandErr, output)
	}
	captureStdout(t, func() {
		commandErr = runSparkwing([]string{"run", "hello", "--sw-allow-unadmitted", "--sw-no-update", "-o=plain"})
	})
	if commandErr == nil || !strings.Contains(commandErr.Error(), "repository has no commits") || !strings.Contains(commandErr.Error(), "git commit --allow-empty") {
		t.Fatalf("first run needs an actionable initial-commit diagnostic: %v", commandErr)
	}
	var diagnostic bytes.Buffer
	writeFailure(&diagnostic, commandErr)
	if !strings.Contains(diagnostic.String(), "git commit --allow-empty") {
		t.Fatalf("rendered refusal lost its repair command: %s", diagnostic.String())
	}
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("planning or refusal created a commit: %s", out)
	}
	runSnapshotGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "--no-verify", "-qm", "Initial commit")
	captureStdout(t, func() {
		commandErr = runSparkwing([]string{"run", "hello", "--sw-allow-unadmitted", "--sw-no-update", "-o=plain"})
	})
	if commandErr != nil {
		t.Fatalf("same scaffold after initial commit: %v", commandErr)
	}
}
