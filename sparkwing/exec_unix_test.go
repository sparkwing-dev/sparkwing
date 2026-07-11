//go:build !windows

package sparkwing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestBash_ContextCancelKillsBackgroundChild(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := sparkwing.Bash(ctx, `
sh -c 'trap "" TERM HUP; while :; do sleep 1; done' &
echo $! > "$PID_FILE"
wait
`).Env("PID_FILE", pidPath).Run()
		done <- err
	}()

	childPID := waitForPIDFile(t, pidPath)
	needsCleanup := true
	t.Cleanup(func() {
		if needsCleanup {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	cancel()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled Bash command did not return")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("cancelled Bash error = %v, want context.Canceled", runErr)
	}
	var execErr *sparkwing.ExecError
	if !errors.As(runErr, &execErr) {
		t.Fatalf("cancelled Bash error type = %T, want *ExecError", runErr)
	}
	if execErr.ExitCode == sparkwing.ExitNotStarted {
		t.Fatalf("cancelled Bash exit code = ExitNotStarted; command started and was cancelled")
	}
	if strings.Contains(runErr.Error(), "failed to start") || !strings.Contains(runErr.Error(), "context canceled") {
		t.Fatalf("cancelled Bash error message = %q, want cancellation cause", runErr.Error())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(childPID) {
			needsCleanup = false
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("background child pid %d survived context cancellation", childPID)
}

func TestBash_ContextCancelReportsCooperativeExitAsCancelled(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := sparkwing.Bash(ctx, `
trap 'exit 0' TERM
echo $$ > "$PID_FILE"
while :; do sleep 1; done
`).Env("PID_FILE", pidPath).Run()
		done <- err
	}()

	_ = waitForPIDFile(t, pidPath)
	cancel()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled Bash command did not return")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("cancelled Bash error = %v, want context.Canceled", runErr)
	}
	if strings.HasPrefix(runErr.Error(), "command failed (exit 0)") || !strings.Contains(runErr.Error(), "context canceled") {
		t.Fatalf("cooperative cancellation message = %q, want cancellation cause", runErr.Error())
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr != nil {
				t.Fatalf("parse pid file: %v", convErr)
			}
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pid file %s", path)
	return 0
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
