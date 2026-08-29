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
		Expired:         func() bool { return true },
		EscalationLimit: 150 * time.Millisecond,
	})

	type outcome struct {
		result ExecResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := execCmd(ctx, os.Args[0], []string{"-test.run=^TestExecCancellationDiagnosticHelper$"}, dir, map[string]string{
			"SPARKWING_EXEC_DIAGNOSTIC_HELPER": "1",
			"SPARKWING_EXEC_READY_FILE":        ready,
			"SPARKWING_EXEC_DESCENDANT_FILE":   descendant,
		})
		finished <- outcome{result: result, err: err}
	}()

	waitForFile(t, ready)
	descendantPID := readPIDFile(t, descendant)
	t.Cleanup(func() { _ = syscall.Kill(descendantPID, syscall.SIGKILL) })
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

func TestExec_NoProgressCancellationCapturesGoRuntimeDump(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	ctx = execdiag.WithPolicy(ctx, execdiag.Policy{
		Expired:         func() bool { return true },
		EscalationLimit: 5 * time.Second,
	})

	type outcome struct {
		result ExecResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := execCmd(ctx, os.Args[0], []string{"-test.run=^TestExecCancellationDiagnosticHelper$"}, dir, map[string]string{
			"SPARKWING_EXEC_RUNTIME_DUMP_HELPER": "1",
			"SPARKWING_EXEC_READY_FILE":          ready,
		})
		finished <- outcome{result: result, err: err}
	}()

	waitForFile(t, ready)
	started := time.Now()
	cancel()

	var got outcome
	select {
	case got = <-finished:
	case <-time.After(6 * time.Second):
		t.Fatal("Go runtime dump did not complete before forced escalation")
	}
	elapsed := time.Since(started)
	t.Logf("captured a 5,000-goroutine runtime dump in %s", elapsed)
	if got.err == nil {
		t.Fatal("diagnostic cancellation returned success")
	}
	if !strings.Contains(got.result.Stderr, "SIGQUIT: quit") {
		t.Fatal("stderr omitted the Go runtime dump header")
	}
	if count := strings.Count(got.result.Stderr, "TestExecCancellationDiagnosticHelper.func"); count < 5_000 {
		t.Fatalf("runtime dump contains %d of 5,000 known blocked goroutines", count)
	}
}

func TestExec_OrdinaryCancellationKeepsImmediateGroupKill(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	descendant := filepath.Join(dir, "descendant")
	ctx, cancel := context.WithCancel(context.Background())
	ctx = execdiag.WithPolicy(ctx, execdiag.Policy{
		Expired:         func() bool { return false },
		EscalationLimit: time.Second,
	})

	type outcome struct {
		result ExecResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := execCmd(ctx, os.Args[0], []string{"-test.run=^TestExecCancellationDiagnosticHelper$"}, dir, map[string]string{
			"SPARKWING_EXEC_DIAGNOSTIC_HELPER": "1",
			"SPARKWING_EXEC_READY_FILE":        ready,
			"SPARKWING_EXEC_DESCENDANT_FILE":   descendant,
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
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ordinary cancellation did not kill the process group immediately")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("ordinary cancellation completed after %s, want immediate group kill", elapsed)
	}
	if got.err == nil {
		t.Fatal("ordinary cancellation returned success")
	}
	if strings.Contains(got.result.Stderr, cancellationDiagnosticMarker) {
		t.Fatalf("ordinary cancellation captured an unexpected diagnostic: %q", got.result.Stderr)
	}
	waitForProcessExit(t, descendantPID)
}

func TestExec_DiagnosticCommandsCannotWriteCoreFiles(t *testing.T) {
	ctx := execdiag.WithPolicy(context.Background(), execdiag.Policy{
		Expired:         func() bool { return false },
		EscalationLimit: time.Second,
		OutputLimit:     1 << 20,
	})
	result, err := execCmd(ctx, os.Args[0], []string{"-test.run=^TestExecCancellationDiagnosticHelper$"}, t.TempDir(), map[string]string{
		"SPARKWING_EXEC_CORE_LIMIT_HELPER": "1",
	})
	if err != nil {
		t.Fatalf("execCmd: %v", err)
	}
	if !strings.Contains(result.Stderr, "core-limit=0") {
		t.Fatalf("stderr = %q, want disabled core-file limit", result.Stderr)
	}
}

func TestDiagnosticOutputLimiter_BoundsOnlyPostTimeoutOutput(t *testing.T) {
	expired := false
	limiter := &diagnosticOutputLimiter{
		policy:    execdiag.Policy{Expired: func() bool { return expired }},
		remaining: 8,
	}
	if got, keep := limiter.filter("ordinary output"); !keep || got != "ordinary output" {
		t.Fatalf("pre-timeout output = %q, %t", got, keep)
	}
	expired = true
	if got, keep := limiter.filter("short"); !keep || got != "short" {
		t.Fatalf("in-budget diagnostic = %q, %t", got, keep)
	}
	if got, keep := limiter.filter("overflow"); !keep || got != diagnosticTruncationMarker {
		t.Fatalf("first overflow = %q, %t", got, keep)
	}
	if got, keep := limiter.filter("more"); keep || got != "" {
		t.Fatalf("post-marker overflow = %q, %t", got, keep)
	}
}

func TestExecCancellationDiagnosticHelper(t *testing.T) {
	if os.Getenv("SPARKWING_EXEC_CORE_LIMIT_HELPER") == "1" {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &limit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "core-limit=%d\n", limit.Cur)
		return
	}
	if os.Getenv("SPARKWING_EXEC_STUBBORN_DESCENDANT") == "1" {
		signal.Ignore(syscall.SIGQUIT)
		if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_READY_FILE"), []byte("ready"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("SPARKWING_EXEC_RUNTIME_DUMP_HELPER") == "1" {
		blocked := make(chan struct{})
		for range 5_000 {
			go func() { <-blocked }()
		}
		if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_READY_FILE"), []byte("ready"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("SPARKWING_EXEC_DIAGNOSTIC_HELPER") != "1" {
		return
	}
	descendantReady := os.Getenv("SPARKWING_EXEC_READY_FILE") + ".descendant"
	descendant := exec.Command(os.Args[0], "-test.run=^TestExecCancellationDiagnosticHelper$")
	descendant.Env = append(os.Environ(),
		"SPARKWING_EXEC_STUBBORN_DESCENDANT=1",
		"SPARKWING_EXEC_READY_FILE="+descendantReady,
	)
	if err := descendant.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(descendantReady); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "descendant did not become ready")
			os.Exit(3)
		}
		time.Sleep(10 * time.Millisecond)
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
