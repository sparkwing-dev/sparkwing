//go:build !windows

package gotoolchain

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

func TestRunKeepsASuccessfulBuildWhenCleanupFails(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}
	failToolchainCleanup(t)

	if err := Run(context.Background(), exec.Command("sh", "-c", "exit 0")); err != nil {
		t.Fatalf("a build that exited zero was reported as failed: %v", err)
	}
}

func TestRunFailsOnANonZeroExit(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}

	err := Run(context.Background(), exec.Command("sh", "-c", "exit 3"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run returned %v, want the command's own exit status", err)
	}
	if exit.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", exit.ExitCode())
	}
}

func TestRunFailsOnANonZeroExitWhenCleanupAlsoFails(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}
	failure := failToolchainCleanup(t)

	err := Run(context.Background(), exec.Command("sh", "-c", "exit 3"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run returned %v, want the command's own exit status", err)
	}
	if errors.Is(err, failure) || errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("run returned the cleanup failure %v instead of the build's own", err)
	}
}

func TestRunReportsCancellation(t *testing.T) {
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
	if err := Run(ctx, exec.Command("sh", "-c", "sleep 10")); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want the cancellation", err)
	}
}

func TestCancelledProbeStopsItsChildren(t *testing.T) {
	if err := procgroup.Supported(); err != nil {
		t.Skipf("process groups unsupported: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.26.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	pidFile := filepath.Join(bin, "child.pid")
	script := "#!/bin/sh\n/bin/sleep 600 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil { //nolint:gosec // an executable stub is the point
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := BuildEnv(ctx, dir, []string{"PATH=" + bin}, "")
		done <- err
	}()
	child := waitForPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("BuildEnv did not return after cancellation")
	}
	if err := syscall.Kill(child, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the probe's child %d survived cancellation: %v", child, err)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}
