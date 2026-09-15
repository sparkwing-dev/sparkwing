package jobs

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestDescribeModuleFailureLeadsWithTheFailingTest(t *testing.T) {
	err := &sparkwing.ExecError{
		Command: "cd \"/repo\" && go test ./...",
		Stdout: strings.Join([]string{
			"ok  \tgithub.com/acme/x/internal/a\t0.2s",
			"--- FAIL: TestServeNativeLifecyclePreservesRunningArtifactAndOptions (1.20s)",
			"    serve_test.go:88: want 1 artifact, got 0",
			"FAIL\tgithub.com/acme/x/cmd/acme\t1.4s",
		}, "\n"),
		ExitCode: 1,
	}

	got := describeModuleFailure("/repo", err)
	lines := strings.Split(got, "\n")

	if !strings.Contains(lines[0], "TestServeNativeLifecyclePreservesRunningArtifactAndOptions") {
		t.Fatalf("the failing test is not on the first line, where the bounded summary keeps it: %q", lines[0])
	}
	if reason, command := strings.Index(got, "want 1 artifact, got 0"), strings.Index(got, "command failed"); reason < 0 || reason > command {
		t.Fatalf("the test reason must precede the generated command: reason=%d command=%d\n%s", reason, command, got)
	}
	if !strings.Contains(lines[2], "FAIL\tgithub.com/acme/x/cmd/acme") {
		t.Fatalf("the failing package is not named after the reason: %q", lines[2])
	}
}

func TestFailedTestNamesSkipsSubtests(t *testing.T) {
	err := &sparkwing.ExecError{
		Stdout: strings.Join([]string{
			"--- FAIL: TestParent (0.10s)",
			"    --- FAIL: TestParent/child (0.01s)",
			"FAIL\tgithub.com/acme/x\t0.2s",
		}, "\n"),
		ExitCode: 1,
	}

	got := failedTestSummaries(err)

	if len(got) != 2 {
		t.Fatalf("got %q, want the top-level test and the package", got)
	}
	for _, line := range got {
		if strings.Contains(line, "TestParent/child") {
			t.Fatalf("a subtest line crowded the names: %q", got)
		}
	}
}

func TestFailedTestNamesAreBounded(t *testing.T) {
	var lines []string
	for i := 0; i < maxNamedTestFailures+10; i++ {
		lines = append(lines, fmt.Sprintf("--- FAIL: TestN%d (0.01s)", i))
	}
	err := &sparkwing.ExecError{Stdout: strings.Join(lines, "\n"), ExitCode: 1}

	got := failedTestSummaries(err)

	if len(got) != maxNamedTestFailures+1 {
		t.Fatalf("got %d names, want %d plus the cut marker", len(got), maxNamedTestFailures)
	}
	if !strings.Contains(got[len(got)-1], "more than") {
		t.Fatalf("a bounded list did not say it was cut: %q", got[len(got)-1])
	}
}

func TestDescribeModuleFailureLeavesANonTestFailureAlone(t *testing.T) {
	err := errors.New("list module packages: exit status 1")

	got := describeModuleFailure("/repo", err)

	if got != "/repo: list module packages: exit status 1" {
		t.Fatalf("got %q", got)
	}
}
