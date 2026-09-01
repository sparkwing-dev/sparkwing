package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gitenv"
	"github.com/sparkwing-dev/sparkwing/internal/githooks"
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

func TestHooksGate_AStepIsShownWhatTheCommitCarries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  []string
		commit []string
		want   string
	}{
		{"git commit -a", nil, []string{"commit", "-am", "two"}, "a.txt\nb.txt"},
		{"git commit -- a.txt", nil, []string{"commit", "-m", "two", "--", "a.txt"}, "a.txt"},
		{"content staged first", []string{"add", "a.txt"}, []string{"commit", "-m", "two"}, "a.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, gateView, inherited := newGateIndexFixture(t)
			if tc.stage != nil {
				f.git(t, tc.stage...)
			}

			if out, err := f.tryGit(f.repo, tc.commit...); err != nil {
				t.Fatalf("commit: %v\n%s", err, out)
			}

			if got := readLines(t, gateView); got != tc.want {
				t.Errorf("the gate step was shown %q, want %q: a staged-diff check passes a commit it never read", got, tc.want)
			}
			if got := readLines(t, inherited); got != "none" {
				t.Errorf("the step inherited GIT_INDEX_FILE=%q; ambient, it is what lets a git add anywhere write into the commit being gated", got)
			}
		})
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

func TestHooksGate_AStepStagingElsewhereLeavesTheGatedRepository(t *testing.T) {
	t.Run("the managed hook keeps the step's work out of the commit", func(t *testing.T) {
		f, elsewhere := newStagingFixture(t)
		captureStdout(t, func() {
			if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
				t.Fatalf("install: %v", err)
			}
		})

		out, err := f.tryGit(f.repo, "commit", "-m", "only a", "--", "a.txt")
		if err != nil {
			t.Errorf("the gate broke the commit it was guarding: %v\n%s", err, out)
		}
		if got := strings.Fields(f.git(t, "show", "--name-only", "--format=", "HEAD")); len(got) != 1 || got[0] != "a.txt" {
			t.Errorf("the gated commit carries %v, want only a.txt: the step staged its files into the repository being gated", got)
		}
		if got := strings.TrimSpace(f.git(t, "-C", elsewhere, "diff", "--cached", "--name-only")); got != "STRANGER.txt" {
			t.Errorf("staging in %s produced %q, want STRANGER.txt: the step's own work went somewhere else", elsewhere, got)
		}
	})

	t.Run("control: the same step without the scrub corrupts the commit", func(t *testing.T) {
		f, elsewhere := newStagingFixture(t)
		hooksDir, err := githooks.Dir(f.repo)
		if err != nil {
			t.Fatal(err)
		}
		writeExec(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\nsparkwing run gate\nexit 0\n")
		if _, err := f.tryGit(f.repo, "config", "core.hooksPath", hooksDir); err != nil {
			t.Fatal(err)
		}

		out, commitErr := f.tryGit(f.repo, "commit", "-m", "only a", "--", "a.txt")
		staged := strings.TrimSpace(f.git(t, "-C", elsewhere, "diff", "--cached", "--name-only"))
		if commitErr == nil {
			t.Errorf("the gated commit survived a step staging through the inherited index, so the scrub this test guards may be obsolete; commit output:\n%s", out)
		}
		if staged != "" {
			t.Errorf("the step's staging landed in %s as %q; without the scrub it goes into the gated repository instead, so this fixture is no longer reproducing the hazard", elsewhere, staged)
		}
	})
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
