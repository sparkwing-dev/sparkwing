//go:build !windows

package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalChildCancellationStopsDescendants(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: exercises process termination grace")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	command := assistedChildHelperCommand("cancel", pidFile, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	outcomes := make(chan assistedChildRunResult, 1)
	go func() {
		err := execLocalChild(ctx, command.Path, dir, command.Args[1:], command.Env)
		outcomes <- assistedChildRunResult{err: err}
	}()
	pid := readAssistedChildPID(t, pidFile)
	t.Cleanup(func() { platformKillAssistedChildTestProcess(pid) })
	cancel()
	if result := awaitAssistedChildOutcome(t, outcomes); result.err == nil {
		t.Fatal("cancelled child reported success")
	}
	if !waitForAssistedChildProcessGone(pid, 2*time.Second) {
		t.Fatalf("descendant %d survived cancellation", pid)
	}
}
