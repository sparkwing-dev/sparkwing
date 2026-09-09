package jobs

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func gitResolutionFixture(t *testing.T) string {
	t.Helper()
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "fixture base")
	runTestGit(t, root, "update-ref", "refs/remotes/"+gateBaselineRef, "HEAD")
	return root
}

func TestGitResolutionKeepsFullCommit(t *testing.T) {
	root := gitResolutionFixture(t)
	commit := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	base, err := resolveGateBase(context.Background())
	if err != nil || base != commit {
		t.Errorf("merge base = %q, %v; want %s", base, err, commit)
	}
	baseline, err := resolveLintBaseline(context.Background())
	if err != nil || !strings.Contains(baseline, commit) {
		t.Errorf("lint baseline = %q, %v; want full %s", baseline, err, commit)
	}
}

func TestGitResolutionPreservesCancellation(t *testing.T) {
	gitResolutionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, resolve := range []func(context.Context) (string, error){resolveGateBase, resolveLintBaseline} {
		_, err := resolve(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("resolution error = %v, want cancellation", err)
		}
	}
}

func TestGitResolutionMissingBaselinePreservesCause(t *testing.T) {
	root := gitResolutionFixture(t)
	runTestGit(t, root, "update-ref", "-d", "refs/remotes/"+gateBaselineRef)
	for _, resolve := range []func(context.Context) (string, error){resolveGateBase, resolveLintBaseline} {
		_, err := resolve(context.Background())
		var exitError *exec.ExitError
		var commandError *sparkwing.ExecError
		if !errors.As(err, &exitError) || !errors.As(err, &commandError) {
			t.Errorf("resolution lost Git cause: %v", err)
			continue
		}
		if commandError.Stderr == "" || !strings.Contains(err.Error(), "git fetch origin main") {
			t.Errorf("resolution lost stderr or fetch guidance: %v", err)
		}
	}
}

func TestGitResolutionNamesUnrelatedHistories(t *testing.T) {
	root := gitResolutionFixture(t)
	unrelated := strings.TrimSpace(gitOutput(t, root, "commit-tree", "HEAD^{tree}", "-m", "separate fixture history"))
	runTestGit(t, root, "update-ref", "refs/remotes/"+gateBaselineRef, unrelated)
	_, err := resolveGateBase(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no common ancestor") || strings.Contains(err.Error(), "git fetch") {
		t.Fatalf("unrelated history error = %v", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Errorf("unrelated history lost Git cause: %v", err)
	}
}

func TestGitResolutionFailureStopsFormatters(t *testing.T) {
	root := gitResolutionFixture(t)
	runTestGit(t, root, "update-ref", "-d", "refs/remotes/"+gateBaselineRef)
	err := runFormatters(context.Background())
	var commandError *sparkwing.ExecError
	if !errors.As(err, &commandError) || !strings.Contains(commandError.Command, "git") {
		t.Fatalf("formatter error = %v, want Git discovery failure", err)
	}
}
