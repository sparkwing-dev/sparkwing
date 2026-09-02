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
	unloadableProject = `pipelines:
  - name: e2e/k8s
    entrypoint: Gate
    on:
      pre_commit: {}
`
)

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

func proverFailingIn(dir string) Prover {
	return func(repoRoot, _ string) error {
		if filepath.Base(repoRoot) == filepath.Base(dir) {
			return errors.New("admission unreachable")
		}
		return nil
	}
}

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
	if !strings.Contains(out, "sparkwing configure xrepo add") {
		t.Errorf("an empty registry should say how to fill it:\n%s", out)
	}
}

func TestInstallFleet_FailsTheRepoWhoseConfigDoesNotLoad(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	broken := f.addRepo(t, "brokenconfig", unloadableProject)
	f.registerRepos(t, f.repo, broken)

	var err error
	out := captureStdout(t, func() { err = installFleet(installOptions{}) })
	if err == nil {
		t.Fatalf("the sweep passed over a repo whose config does not load:\n%s", out)
	}
	if !strings.Contains(err.Error(), "brokenconfig") {
		t.Errorf("the failure does not name the repo: %v", err)
	}
	if !strings.Contains(out, "config does not load") {
		t.Errorf("the sweep does not say why the repo was skipped:\n%s", out)
	}
	if !strings.Contains(out, "1 repo(s) armed, 0 with no gate to arm, 0 left ungated, 1 failed") {
		t.Errorf("an unloadable repo counted as one with no gate to arm:\n%s", out)
	}
}

func TestSurveyFleet_ReportsAnUnloadableConfigRatherThanNoGates(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	broken := f.addRepo(t, "brokenconfig", unloadableProject)
	f.registerRepos(t, broken)

	rows, err := surveyFleet(f.tryGit)
	if err != nil {
		t.Fatalf("surveyFleet: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one row", rows)
	}
	if rows[0].State != githooks.GateBroken {
		t.Fatalf("State = %s, want %s", rows[0].State, githooks.GateBroken)
	}
	if rows[0].Gated() {
		t.Error("a repo whose config does not load reported as gated")
	}
	var out strings.Builder
	if err := renderHooksSurvey(&out, rows, "pretty"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	if !strings.Contains(out.String(), "config does not load") {
		t.Errorf("the survey does not say the config is the problem:\n%s", out.String())
	}
}

func TestStatusHooks_FailsWhenTheConfigNoLongerLoads(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), unloadableProject)

	var err error
	out := captureStdout(t, func() { err = statusHooks(f.tryGit, f.repo) })
	if err == nil {
		t.Fatalf("status reported an unloadable repo as an ungated but healthy one:\n%s", out)
	}
	if !strings.Contains(err.Error(), "config does not load") {
		t.Errorf("error should say the config does not load: %v", err)
	}
	if !strings.Contains(out, "pre-commit -> gate") {
		t.Errorf("status should still name the stale hook on disk:\n%s", out)
	}
}
