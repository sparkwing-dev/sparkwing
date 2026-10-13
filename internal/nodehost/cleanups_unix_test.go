//go:build !windows

package nodehost_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// safety: the child ignores SIGTERM so only the escalation to SIGKILL ends it.
const stubbornChild = `(trap '' TERM; exec sleep 300) & echo $! > "$1"; `

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
	s, _ := start(t)
	bearer, revoke := cleanupNode(t, s)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	mustRegister(t, s, bearer, "sh", "-c", stubbornChild+"exit 0", "sh", pidFile)
	revoke()
	var log syncBuffer
	if err := s.RunCleanups(context.Background(), bearer, nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	requireChildGone(t, pidFile)
	if strings.Contains(log.String(), "failed") {
		t.Fatalf("a cleanup that exited 0 was logged as failed:\n%s", log.String())
	}
}

func TestCancelledCleanupTakesItsBackgroundChildWithIt(t *testing.T) {
	s, _ := start(t)
	bearer, revoke := cleanupNode(t, s)
	dir := t.TempDir()
	pidFile, ready := filepath.Join(dir, "child.pid"), filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	mustRegister(t, s, bearer, "sh", "-c", stubbornChild+`echo up > "$2"; exec sleep 300`, "sh", pidFile, ready)
	revoke()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// safety: opening the fifo blocks until the cleanup has started its child.
		_, _ = os.ReadFile(ready)
		cancel()
	}()
	var log syncBuffer
	if err := s.RunCleanups(ctx, bearer, nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	requireChildGone(t, pidFile)
	if !strings.Contains(log.String(), "cleanup sh failed: cancelled") {
		t.Fatalf("cleanup log:\n%s\nwant the cancellation", log.String())
	}
}
