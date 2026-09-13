package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func commentFixtureRepo(t *testing.T) string {
	t.Helper()
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/"+gateBaselineRef, "HEAD")
	unsetForTest(t, "GIT_INDEX_FILE")
	unsetForTest(t, gateIndexVar)
	return root
}

func TestCommentStepGatesTheRangeWhenNothingIsStaged(t *testing.T) {
	root := commentFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "committed.go"),
		"package internal\n\nfunc committed() int { return 2 }\n")
	gitCommitAll(t, root, "work the branch already carries")

	command, scope, err := commentCheckCommand(context.Background())
	if err != nil {
		t.Fatalf("commentCheckCommand: %v", err)
	}
	if strings.Contains(command, "-staged") {
		t.Fatalf("the step gated the staged diff of a clean committed branch, which is empty: %q", command)
	}
	if !strings.Contains(command, "-base ") {
		t.Errorf("command = %q, want the range mode", command)
	}
	if !strings.Contains(scope, gateBaselineRef) {
		t.Errorf("scope = %q, does not name the baseline the step gated", scope)
	}
}

func TestCommentStepGatesTheStagedDiffWhenACommitIsBeingBuilt(t *testing.T) {
	root := commentFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "pending.go"),
		"package internal\n\nfunc pending() int { return 3 }\n")
	writeGoFile(t, filepath.Join(root, "notes.md"), "# notes\n")
	gitAddAll(t, root)

	command, scope, err := commentCheckCommand(context.Background())
	if err != nil {
		t.Fatalf("commentCheckCommand: %v", err)
	}
	if !strings.Contains(command, "-staged") {
		t.Errorf("command = %q, want the staged mode a commit hook needs", command)
	}
	if !strings.Contains(scope, "1 staged Go file(s)") {
		t.Errorf("scope = %q, does not count the Go files the step judged", scope)
	}
}

func TestCommentStepReadsTheIndexTheCommitIsBeingBuiltIn(t *testing.T) {
	root := commentFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "pending.go"),
		"package internal\n\nfunc pending() int { return 4 }\n")

	index := filepath.Join(t.TempDir(), "next-index.lock")
	data, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGitWithIndex(t, root, index, "add", "internal/pending.go")
	t.Setenv(gateIndexVar, index)

	command, _, err := commentCheckCommand(context.Background())
	if err != nil {
		t.Fatalf("commentCheckCommand: %v", err)
	}
	if !strings.Contains(command, "-staged") {
		t.Fatalf("the step read the repository index, so `git commit -a` looks like it stages nothing: %q", command)
	}
}

func TestCommentStepLetsACallerBindItsOwnIndex(t *testing.T) {
	commentFixtureRepo(t)
	bound := filepath.Join(t.TempDir(), "caller-index")
	t.Setenv("GIT_INDEX_FILE", bound)
	t.Setenv(gateIndexVar, filepath.Join(t.TempDir(), "gate-index"))

	if got := hookIndex(); got != "" {
		t.Fatalf("hookIndex() = %q; it overrode the index the caller bound at %s", got, bound)
	}
}

func TestCommentStepRefusesWhenTheBaselineIsMissing(t *testing.T) {
	root := commentFixtureRepo(t)
	runTestGit(t, root, "update-ref", "-d", "refs/remotes/"+gateBaselineRef)

	_, _, err := commentCheckCommand(context.Background())
	if err == nil {
		t.Fatal("the step chose a range it cannot resolve")
	}
	for _, want := range []string{"could not run", gateBaselineRef, "git fetch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q omits %q", err.Error(), want)
		}
	}
}

func runTestGitWithIndex(t *testing.T, dir, index string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func unsetForTest(t *testing.T, name string) {
	t.Helper()
	// safety: t.Setenv cannot clear a variable, and git reads an empty
	// GIT_INDEX_FILE as a path it cannot write. This drops t.Setenv's
	// parallel-test guard with it; no test in this package runs in parallel.
	prev, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, prev)
			return
		}
		_ = os.Unsetenv(name)
	})
}
