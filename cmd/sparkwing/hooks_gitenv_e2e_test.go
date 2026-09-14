//go:build e2e

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

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
