//go:build !windows

package nodehost_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/procgroup"
)

// safety: the child ignores SIGTERM so only the escalation to SIGKILL ends it.
const stubbornChild = `(trap '' TERM; exec cat "$2") & echo $! > "$1"; `

func requireChildGone(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the cleanup wrote no child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	processes, err := procgroup.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range processes {
		if p.PID == pid && !p.Terminated() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the cleanup's background child %d outlived it", pid)
		}
	}
}

func TestCleanupsBackgroundChildDoesNotOutliveIt(t *testing.T) {
	cleanups := &nodehost.ProcessCleanups{}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	mustRegisterCleanup(t, cleanups, "sh", "-c", stubbornChild+"exit 0", "sh", pidFile, cleanupPipe(t, dir, "hold").Name())
	var log bytes.Buffer
	if err := cleanups.Run(cleanupContext(t), nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv(t)}); err != nil {
		t.Fatal(err)
	}
	requireChildGone(t, pidFile)
	if strings.Contains(log.String(), "failed") {
		t.Fatalf("a cleanup that exited 0 was logged as failed:\n%s", log.String())
	}
}

func cleanupPipe(t *testing.T, dir, name string) *os.File {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipe.Close() })
	return pipe
}

func TestCancelledCleanupTakesItsBackgroundChildWithIt(t *testing.T) {
	cleanups := &nodehost.ProcessCleanups{}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	hold, ready := cleanupPipe(t, dir, "hold"), cleanupPipe(t, dir, "ready")
	if err := ready.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	mustRegisterCleanup(t, cleanups, "sh", "-c", stubbornChild+`echo up > "$3"; exec cat "$2"`, "sh", pidFile, hold.Name(), ready.Name())
	ctx, cancel := context.WithCancel(cleanupContext(t))
	defer cancel()
	read := make(chan error, 1)
	go func() {
		var data [3]byte
		_, err := ready.Read(data[:])
		read <- err
		cancel()
	}()
	var log bytes.Buffer
	if err := cleanups.Run(ctx, nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv(t)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup cancellation error=%v", err)
	}
	if err := <-read; err != nil {
		t.Fatalf("cleanup readiness: %v", err)
	}
	requireChildGone(t, pidFile)
	if !strings.Contains(log.String(), "cleanup sh failed: cancelled") {
		t.Fatalf("cleanup log:\n%s\nwant the cancellation", log.String())
	}
}

func TestCleanupIsKilledAtItsDeadline(t *testing.T) {
	cleanups := &nodehost.ProcessCleanups{}
	dir := t.TempDir()
	hold := cleanupPipe(t, dir, "hold")
	mustRegisterCleanup(t, cleanups, "sh", "-c", `exec cat "$1"`, "sh", hold.Name(), "s3cret-arg")
	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := cleanups.Run(ctx, nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv(t)}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup deadline error=%v", err)
	}
	if out := log.String(); !strings.Contains(out, "cleanup sh failed: timed out") || strings.Contains(out, "s3cret-arg") {
		t.Fatalf("cleanup log:\n%s\nwant a timeout naming only sh", out)
	}
}
