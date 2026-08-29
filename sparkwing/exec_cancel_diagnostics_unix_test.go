//go:build unix

package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/execdiag"
)

const cancellationDiagnosticMarker = "cancellation diagnostic captured"

func TestExec_NoProgressCancellationCapturesDiagnosticBeforeGroupKill(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	descendant := filepath.Join(dir, "descendant")
	ctx, cancel := context.WithCancel(context.Background())
	ctx = execdiag.WithPolicy(ctx, execdiag.Policy{
		Expired: func() bool { return true },
		Grace:   150 * time.Millisecond,
	})

	type outcome struct {
		result ExecResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := execCmd(ctx, os.Args[0], []string{"-test.run=^TestExecCancellationDiagnosticHelper$"}, dir, map[string]string{
			"SPARKWING_EXEC_DIAGNOSTIC_HELPER": "1",
			"SPARKWING_EXEC_READY_FILE":       ready,
			"SPARKWING_EXEC_DESCENDANT_FILE":  descendant,
		})
		finished <- outcome{result: result, err: err}
	}()

	waitForFile(t, ready)
	descendantPID := readPIDFile(t, descendant)
	started := time.Now()
	cancel()

	var got outcome
	select {
	case got = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic cancellation did not force-kill the process group")
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond || elapsed > time.Second {
		t.Fatalf("diagnostic cancellation completed after %s, want a bounded collection window", elapsed)
	}
	if got.err == nil {
		t.Fatal("diagnostic cancellation returned success")
	}
	if !strings.Contains(got.result.Stderr, cancellationDiagnosticMarker) {
		t.Fatalf("stderr = %q, want cancellation diagnostic", got.result.Stderr)
	}
	waitForProcessExit(t, descendantPID)
}

func TestExecCancellationDiagnosticHelper(t *testing.T) {
	if os.Getenv("SPARKWING_EXEC_DIAGNOSTIC_HELPER") != "1" {
		return
	}
	descendant := exec.Command("sleep", "3600")
	if err := descendant.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_DESCENDANT_FILE"), []byte(strconv.Itoa(descendant.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGQUIT)
	go func() {
		<-quit
		fmt.Fprintln(os.Stderr, cancellationDiagnosticMarker)
	}()
	if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_READY_FILE"), []byte("ready"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	select {}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant %d survived diagnostic escalation", pid)
}
