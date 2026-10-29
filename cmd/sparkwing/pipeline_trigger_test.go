package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	fn()
	w.Close()
	return string(<-done)
}

func enterGitCheckout(t *testing.T, origin string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgSign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "sparkwing@example.invalid")
	run("config", "user.name", "Sparkwing Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	run("branch", "-M", "main")
	run("remote", "add", "origin", origin)
	run("update-ref", "refs/remotes/origin/main", "HEAD")

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

// --runner cloud must select the Cloud claimant class, because no Cloud
// launcher holds a runner name the run could be sealed to.
func TestRunnerFlagCloudSelectsTheCloudClass(t *testing.T) {
	for flag, want := range map[string]runspec.RunnerSelection{
		"cloud":       {Kind: runspec.RunnerLabel, Value: "class=cloud"},
		"here":        {Kind: runspec.RunnerHere},
		"label=gpu":   {Kind: runspec.RunnerLabel, Value: "gpu"},
		"build-box-1": {Kind: runspec.RunnerName, Value: "build-box-1"},
	} {
		if got := runnerFlagSelection(flag); got != want {
			t.Errorf("--runner %s = %+v, want %+v", flag, got, want)
		}
	}
}
