package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

func TestDispatch_UnbindsFromTheRepositoryThatLaunchedIt(t *testing.T) {
	index := filepath.Join(t.TempDir(), "next-index-1234.lock")
	writeRepoFile(t, index, "index\n")
	bound := map[string]string{
		"GIT_DIR":        "/gating/.git",
		"GIT_INDEX_FILE": index,
		"GIT_WORK_TREE":  "/gating",
	}
	for name, value := range bound {
		t.Setenv(name, value)
	}
	t.Setenv("GIT_AUTHOR_NAME", "sparkwing test")
	t.Setenv(gitenv.GateIndexVar, "")
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_REPOS", filepath.Join(t.TempDir(), "repos.yaml"))

	_ = runSparkwing([]string{"run"})

	for name := range bound {
		if got, ok := os.LookupEnv(name); ok {
			t.Errorf("%s survived dispatch as %q; every process the pipeline starts would inherit it", name, got)
		}
	}
	if got := os.Getenv("GIT_AUTHOR_NAME"); got != "sparkwing test" {
		t.Errorf("GIT_AUTHOR_NAME = %q, want it untouched: identity names no repository", got)
	}
	if got := gitenv.GateIndex(); got != index {
		t.Errorf("the gate index came out as %q, want %q: a step scoped to the staged diff has nothing left to read", got, index)
	}
}

func newGateIndexFixture(t *testing.T) (f *chainFixture, gateView, inherited string) {
	t.Helper()
	f = newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, "a.txt"), "a\n")
	writeRepoFile(t, filepath.Join(f.repo, "b.txt"), "b\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "base")
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("install: %v", err)
		}
	})

	gateView = filepath.Join(f.root, "gate-view")
	inherited = filepath.Join(f.root, "inherited-index")
	writeExec(t, filepath.Join(f.binDir, "sparkwing"),
		"#!/bin/sh\n"+
			"echo \"${GIT_INDEX_FILE:-none}\" > "+inherited+"\n"+
			"GIT_INDEX_FILE=\"${"+gitenv.GateIndexVar+":-}\" git diff --cached --name-only > "+gateView+"\n"+
			"exit 0\n")
	writeRepoFile(t, filepath.Join(f.repo, "a.txt"), "a2\n")
	writeRepoFile(t, filepath.Join(f.repo, "b.txt"), "b2\n")
	return f, gateView, inherited
}

func readLines(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the gate never ran: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func newStagingFixture(t *testing.T) (*chainFixture, string) {
	t.Helper()
	f := newChainFixture(t)
	elsewhere := filepath.Join(f.root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := f.tryGit(elsewhere, "init", "-b", "main"); err != nil {
		t.Fatalf("git init %s: %v\n%s", elsewhere, err, out)
	}
	writeRepoFile(t, filepath.Join(elsewhere, "STRANGER.txt"), "a stranger\n")
	writeRepoFile(t, filepath.Join(f.repo, "a.txt"), "a\n")
	writeRepoFile(t, filepath.Join(f.repo, "b.txt"), "b\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "base")

	writeExec(t, filepath.Join(f.binDir, "sparkwing"),
		"#!/bin/sh\necho \"$@\" >> "+f.ranFile+"\ngit -C "+elsewhere+" add -A\n")
	writeRepoFile(t, filepath.Join(f.repo, "a.txt"), "a2\n")
	writeRepoFile(t, filepath.Join(f.repo, "b.txt"), "b2\n")
	return f, elsewhere
}
