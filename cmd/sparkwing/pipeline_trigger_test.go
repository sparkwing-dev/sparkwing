package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const triggerTestOrigin = "https://github.com/acme/sample-app.git"

func writeTriggerProfiles(t *testing.T, controllerURL string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "profiles:\n" +
		"  prod: { controller: { url: " + controllerURL + ", token: swu_test } }\n" +
		"  laptop: { state: { type: sqlite } }\n"
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
}

func writeTriggerProfilesWithLogs(t *testing.T, controllerURL string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "profiles:\n" +
		"  prod: { controller: { url: " + controllerURL + " } }\n"
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
}

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

func withGitCheckout(t *testing.T, origin string, fn func()) {
	t.Helper()
	enterGitCheckout(t, origin)
	fn()
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

// Staging qa.8: --runner cloud sealed a runner name no Cloud launcher holds,
// so the run stayed unclaimed.
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
