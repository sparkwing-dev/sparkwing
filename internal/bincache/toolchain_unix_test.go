//go:build !windows

package bincache

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func failToolchainCleanup(t *testing.T) error {
	t.Helper()
	failure := errors.New("process table unavailable")
	original := toolchainDescendantProbe
	t.Cleanup(func() { toolchainDescendantProbe = original })
	toolchainDescendantProbe = func(int, bool, bool) (bool, error) { return false, failure }
	return failure
}

func TestRunToolchainKeepsASuccessfulBuildWhenCleanupFails(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}
	failToolchainCleanup(t)

	if err := runToolchain(context.Background(), exec.Command("sh", "-c", "exit 0")); err != nil {
		t.Fatalf("a build that exited zero was reported as failed: %v", err)
	}
}

func TestRunToolchainFailsOnANonZeroExit(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}

	err := runToolchain(context.Background(), exec.Command("sh", "-c", "exit 3"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run returned %v, want the command's own exit status", err)
	}
	if exit.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", exit.ExitCode())
	}
}

func TestRunToolchainFailsOnANonZeroExitWhenCleanupAlsoFails(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}
	failure := failToolchainCleanup(t)

	err := runToolchain(context.Background(), exec.Command("sh", "-c", "exit 3"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run returned %v, want the command's own exit status", err)
	}
	if errors.Is(err, failure) || errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("run returned the cleanup failure %v instead of the build's own", err)
	}
}

func TestRunToolchainReportsCancellation(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}
	failToolchainCleanup(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := runToolchain(ctx, exec.Command("sh", "-c", "sleep 10")); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want the cancellation", err)
	}
}
