package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type chainFixture struct {
	root    string
	repo    string
	binDir  string
	ranFile string
}

func newChainFixture(t *testing.T) *chainFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	f := &chainFixture{
		root:    root,
		repo:    filepath.Join(root, "proj"),
		binDir:  filepath.Join(root, "bin"),
		ranFile: filepath.Join(root, "pipelines-run"),
	}

	for _, dir := range []string{f.repo, f.binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExec(t, filepath.Join(f.binDir, "sparkwing"),
		"#!/bin/sh\necho \"$@\" >> "+shellSingleQuote(filepath.ToSlash(f.ranFile))+"\nexit 0\n")
	writeRepoFile(t, filepath.Join(root, "gitconfig"), "")
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

func (f *chainFixture) env() []string {
	return []string{
		"PATH=" + f.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + f.root,
		"GIT_CONFIG_GLOBAL=" + filepath.Join(f.root, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

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
	args = append([]string{"-c", "user.name=sparkwing test", "-c", "user.email=test@example.invalid"}, args...)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if dir == "" {
		cmd.Dir = f.repo
	}
	cmd.Env = f.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}
