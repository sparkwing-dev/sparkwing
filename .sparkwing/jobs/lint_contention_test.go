package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestLintCommandWaitsForTheBoxWideLockInsteadOfFailingOnIt(t *testing.T) {
	if !strings.Contains(lintCommand, "--allow-serial-runners") {
		t.Fatalf("the gate would fail on a neighbouring lint instead of waiting for it: %s", lintCommand)
	}
}

func TestDescribeLintFailureOnAnExpiredWaitReportsTheWaitNotACause(t *testing.T) {
	got := describeLintFailure(expiredContext(t), 7*time.Second, errors.New("command failed (exit 1)"))

	if !strings.Contains(got, "could not run") {
		t.Fatalf("expired wait did not report could-not-run: %s", got)
	}
	if !strings.Contains(got, "7s") {
		t.Fatalf("expired wait did not report how long it actually waited: %s", got)
	}
	if strings.Contains(got, lintLockWait.String()) {
		t.Fatalf("expired wait quoted the bound rather than the wait it measured: %s", got)
	}
	if strings.Contains(got, "That is contention") {
		t.Fatalf("expired wait asserted a cause it never observed: %s", got)
	}
	if strings.Contains(got, "exit 1") {
		t.Fatalf("expired wait still reported a bare exit code: %s", got)
	}
}

func TestDescribeLintFailureNamesContentionFromTheLinterOwnMessage(t *testing.T) {
	err := &sparkwing.ExecError{
		Command:  "golangci-lint run ./...",
		Stderr:   "Error: parallel golangci-lint is running\n",
		ExitCode: 3,
	}

	got := describeLintFailure(context.Background(), time.Second, err)

	if !strings.Contains(got, "could not run") {
		t.Fatalf("contention signature did not report could-not-run: %s", got)
	}
	if !strings.Contains(got, "contention") {
		t.Fatalf("contention signature did not name contention as the cause: %s", got)
	}
}

func TestDescribeLintFailureReportsRealFindingsUnchanged(t *testing.T) {
	err := &sparkwing.ExecError{
		Command:  "golangci-lint run ./...",
		Stdout:   "main.go:7:2: declared and not used: x (typecheck)\n",
		ExitCode: 1,
	}

	got := describeLintFailure(context.Background(), time.Second, err)

	if strings.Contains(got, "could not run") {
		t.Fatalf("a genuine finding was excused as contention: %s", got)
	}
	if !strings.Contains(got, "golangci-lint:") {
		t.Fatalf("finding lost its golangci-lint attribution: %s", got)
	}
}

// expiredContext returns a context whose deadline has already passed,
// which is the state the lint step is in when its bounded wait runs out.
func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	t.Cleanup(cancel)
	<-ctx.Done()
	return ctx
}
