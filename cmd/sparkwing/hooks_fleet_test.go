package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

// gateProject declares a commit gate; noTriggerProject declares a pipeline
// nothing runs it from, which is the shape of a repo a fleet sweep has
// nothing to arm in.
const (
	gateProject = `pipelines:
  - name: gate
    entrypoint: Gate
    on:
      pre_commit: {}
`
	noTriggerProject = `pipelines:
  - name: build
    entrypoint: Build
`
)

// addRepo builds another checkout beside the fixture's own, so a fleet sweep
// has more than one repo to reach.
func (f *chainFixture) addRepo(t *testing.T, name, project string) string {
	t.Helper()
	dir := filepath.Join(f.root, name)
	writeRepoFile(t, filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), project)
	writeRepoFile(t, filepath.Join(dir, "README.md"), "hello\n")
	if out, err := f.tryGit(dir, "init", "-b", "main"); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
	return dir
}

// registerRepos writes the machine registry a fleet sweep enumerates.
func (f *chainFixture) registerRepos(t *testing.T, dirs ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("repos:\n")
	for _, d := range dirs {
		fmt.Fprintf(&b, "  - path: %s\n", d)
	}
	path := filepath.Join(f.root, "repos.yaml")
	writeRepoFile(t, path, b.String())
	t.Setenv("SPARKWING_REPOS", path)
}

// proverFailingIn fails the proof for one repo and passes everywhere else.
func proverFailingIn(dir string) Prover {
	return func(repoRoot, _ string) error {
		if filepath.Base(repoRoot) == filepath.Base(dir) {
			return errors.New("admission unreachable")
		}
		return nil
	}
}

// A sweep that reports repos it left ungated as installed is the silent,
// partial coverage the sweep exists to end, wearing a green summary.
func TestInstallFleet_CountsWhatItArmedAndNamesWhatItDidNot(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	red := f.addRepo(t, "redgate", gateProject)
	quiet := f.addRepo(t, "notriggers", noTriggerProject)
	f.registerRepos(t, f.repo, red, quiet)

	var err error
	out := captureStdout(t, func() {
		err = installFleet(installOptions{prove: proverFailingIn(red)})
	})
	if err != nil {
		t.Fatalf("installFleet: %v", err)
	}
	if !strings.Contains(out, "1 repo(s) armed, 1 with no gate to arm, 1 left ungated, 0 failed") {
		t.Errorf("summary does not match what the sweep did:\n%s", out)
	}
	if !strings.Contains(out, "still ungated: redgate") {
		t.Errorf("the sweep did not name the repo it left ungated:\n%s", out)
	}
}

// A repo whose only hook runs after the commit has landed is neither armed
// nor left ungated: there is no gate in it for a re-run to arm, and calling
// it armed reports a swept fleet as gated while its commits go unchecked.
func TestInstallFleet_CountsARepoWithNoGateApartFromTheOnesItArmed(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	notifier := f.addRepo(t, "notifier", notifyOnlyProject)
	f.registerRepos(t, f.repo, notifier)

	out := captureStdout(t, func() {
		if err := installFleet(installOptions{}); err != nil {
			t.Fatalf("installFleet: %v", err)
		}
	})
	if !strings.Contains(out, "1 repo(s) armed, 1 with no gate to arm, 0 left ungated, 0 failed") {
		t.Errorf("summary counts a repo with no gate among the armed:\n%s", out)
	}
	if strings.Contains(out, "still ungated: notifier") {
		t.Errorf("the sweep asks for a re-run of a repo with nothing to arm:\n%s", out)
	}
}

// The repo whose gate could not run must come out of the sweep exactly as it
// went in: unclaimed, so its commits still land, and with the hooks it had.
func TestInstallFleet_LeavesTheRepoWhoseGateCannotRunUnarmed(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	red := f.addRepo(t, "redgate", gateProject)
	f.registerRepos(t, f.repo, red)

	captureStdout(t, func() {
		if err := installFleet(installOptions{prove: proverFailingIn(red)}); err != nil {
			t.Fatalf("installFleet: %v", err)
		}
	})
	if got, err := f.tryGit(red, "config", "--local", "core.hooksPath"); err == nil {
		t.Errorf("the sweep armed a repo whose gate cannot run: core.hooksPath = %q", strings.TrimSpace(got))
	}
	want := filepath.Join(f.repo, ".git", "hooks")
	if got := localHooksPath(t, f); !githooks.SameDir(got, want) {
		t.Errorf("core.hooksPath = %q, want %q: the repo whose gate proved should be armed", got, want)
	}
}

// The sweep answers for the checkouts the machine registered and no others,
// which is the limit of what it can claim to cover.
func TestInstallFleet_SweepsOnlyTheRegisteredCheckouts(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	unregistered := f.addRepo(t, "unregistered", gateProject)
	f.registerRepos(t, f.repo)

	out := captureStdout(t, func() {
		if err := installFleet(installOptions{}); err != nil {
			t.Fatalf("installFleet: %v", err)
		}
	})
	if strings.Contains(out, "unregistered") {
		t.Errorf("the sweep reached a checkout the registry does not list:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(unregistered, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Errorf("the sweep installed into an unregistered checkout (stat err = %v)", err)
	}
}

func TestInstallFleet_SaysSoWhenTheRegistryIsEmpty(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	f.registerRepos(t)

	out := captureStdout(t, func() {
		if err := installFleet(installOptions{}); err != nil {
			t.Fatalf("installFleet: %v", err)
		}
	})
	if !strings.Contains(out, "sparkwing pipeline add") {
		t.Errorf("an empty registry should say how to fill it:\n%s", out)
	}
}
