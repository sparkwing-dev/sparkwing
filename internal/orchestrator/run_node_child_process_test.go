package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const assistedChildHelperMode = "SPARKWING_ASSISTED_CHILD_HELPER"

const assistedChildHelperPIDFile = "SPARKWING_ASSISTED_CHILD_PID_FILE"

const assistedChildHelperStubborn = "SPARKWING_ASSISTED_CHILD_STUBBORN"

const assistedChildHelperMarker = "SPARKWING_ASSISTED_CHILD_MARKER"

func TestAssistedChildProcessHelper(t *testing.T) {
	switch os.Getenv(assistedChildHelperMode) {
	case "descendant":
		if os.Getenv(assistedChildHelperStubborn) == "1" {
			platformIgnoreAssistedChildTermination()
		}
		if err := os.WriteFile(
			os.Getenv(assistedChildHelperPIDFile),
			[]byte(strconv.Itoa(os.Getpid())),
			0o600,
		); err != nil {
			os.Exit(2)
		}
		blockAssistedChildTestProcess()
	case "cancel", "exit":
		mode := os.Getenv(assistedChildHelperMode)
		child := exec.Command(os.Args[0], "-test.run=^TestAssistedChildProcessHelper$")
		child.Env = append(os.Environ(), assistedChildHelperMode+"=descendant")
		platformConfigureAssistedChildDescendant(child)
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := waitForAssistedChildPIDFile(os.Getenv(assistedChildHelperPIDFile), 3*time.Second); err != nil {
			_ = child.Process.Kill()
			os.Exit(2)
		}
		if mode == "exit" {
			os.Exit(0)
		}
		platformIgnoreAssistedChildTermination()
		blockAssistedChildTestProcess()
	case "mark":
		if err := os.WriteFile(os.Getenv(assistedChildHelperMarker), []byte("started"), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "breakaway":
		result := "denied"
		if platformAttemptAssistedChildBreakaway() == nil {
			result = "escaped"
		}
		if err := os.WriteFile(os.Getenv(assistedChildHelperMarker), []byte(result), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func blockAssistedChildTestProcess() {
	for {
		time.Sleep(time.Hour)
	}
}

func TestAssistedChildCancellationWaitsForStubbornDescendant(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	cmd := assistedChildHelperCommand("cancel", pidFile, true)
	ctx, cancel := context.WithCancelCause(context.Background())
	outcomes := make(chan assistedChildRunResult, 1)
	go func() {
		outcome, err := runAssistedChildProcess(ctx, cmd, discardAssistedChildLogger())
		outcomes <- assistedChildRunResult{outcome: outcome, err: err}
	}()

	descendantPID := readAssistedChildPID(t, pidFile)
	t.Cleanup(func() { platformKillAssistedChildTestProcess(descendantPID) })
	cause := errors.New("executor lease lost")
	cancel(cause)

	result := awaitAssistedChildOutcome(t, outcomes)
	if result.err != nil {
		t.Fatalf("run assisted child: %v", result.err)
	}
	if !errors.Is(result.outcome.cancelCause, cause) {
		t.Fatalf("cancel cause = %v, want %v", result.outcome.cancelCause, cause)
	}
	if !waitForAssistedChildProcessGone(descendantPID, 2*time.Second) {
		t.Fatalf("descendant %d survived cancellation cleanup", descendantPID)
	}
}

func TestAssistedChildNormalExitCleansRemainingDescendant(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	cmd := assistedChildHelperCommand("exit", pidFile, false)
	outcomes := make(chan assistedChildRunResult, 1)
	go func() {
		outcome, err := runAssistedChildProcess(context.Background(), cmd, discardAssistedChildLogger())
		outcomes <- assistedChildRunResult{outcome: outcome, err: err}
	}()

	descendantPID := readAssistedChildPID(t, pidFile)
	t.Cleanup(func() { platformKillAssistedChildTestProcess(descendantPID) })
	result := awaitAssistedChildOutcome(t, outcomes)
	if result.err != nil || result.outcome.cancelCause != nil || result.outcome.waitErr != nil {
		t.Fatalf("normal assisted child result = %+v err=%v", result.outcome, result.err)
	}
	if !waitForAssistedChildProcessGone(descendantPID, 2*time.Second) {
		t.Fatalf("descendant %d survived normal-exit cleanup", descendantPID)
	}
}

func TestAssistedChildAlreadyCanceledDoesNotStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAssistedChildProcessHelper$")
	cmd.Env = append(os.Environ(), assistedChildHelperMode+"=mark", assistedChildHelperMarker+"="+marker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome, err := runAssistedChildProcess(ctx, cmd, discardAssistedChildLogger())
	if err != nil || !errors.Is(outcome.cancelCause, context.Canceled) {
		t.Fatalf("pre-canceled result = %+v err=%v", outcome, err)
	}
	if cmd.Process != nil {
		t.Fatalf("pre-canceled child started with pid %d", cmd.Process.Pid)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pre-canceled child wrote marker: %v", err)
	}
}

func TestAssistedChildStartFailureReturnsWithoutAProcess(t *testing.T) {
	cmd := exec.Command(filepath.Join(t.TempDir(), "missing-runner"))
	outcome, err := runAssistedChildProcess(context.Background(), cmd, discardAssistedChildLogger())
	if err == nil {
		t.Fatalf("missing child result = %+v, want start error", outcome)
	}
	if cmd.Process != nil {
		t.Fatalf("failed child start retained pid %d", cmd.Process.Pid)
	}
}

func TestFailedAssistedChildCleanupLogsAndRetriesInspectionAndTermination(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	queryCalls := 0
	terminateCalls := 0
	waited := false
	var sleeps []time.Duration
	failedAssistedChildCleanup{
		inspect: func() (bool, error) {
			queryCalls++
			switch queryCalls {
			case 1:
				return false, errors.New("query unavailable")
			case 2, 3, 4:
				return false, nil
			default:
				return true, nil
			}
		},
		terminate: func() error {
			terminateCalls++
			if terminateCalls == 1 {
				return errors.New("termination unavailable")
			}
			return nil
		},
		wait:          func() { waited = true },
		sleep:         func(delay time.Duration) { sleeps = append(sleeps, delay) },
		processID:     42,
		boundary:      "job_object",
		inspectAction: "query_job",
		stopAction:    "terminate_job",
	}.settle(logger)

	if queryCalls != 5 || terminateCalls != 2 || !waited {
		t.Fatalf("cleanup calls: query=%d terminate=%d waited=%v", queryCalls, terminateCalls, waited)
	}
	wantSleeps := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	if fmt.Sprint(sleeps) != fmt.Sprint(wantSleeps) {
		t.Fatalf("cleanup backoff = %v, want %v", sleeps, wantSleeps)
	}
	logText := logs.String()
	if !strings.Contains(logText, "operation=query_job") || !strings.Contains(logText, "operation=terminate_job") {
		t.Fatalf("cleanup logs do not distinguish operations: %s", logText)
	}
}

func TestFailedAssistedChildCleanupBackoffIsBounded(t *testing.T) {
	queryCalls := 0
	waited := false
	var sleeps []time.Duration
	failedAssistedChildCleanup{
		inspect: func() (bool, error) {
			queryCalls++
			if queryCalls <= 9 {
				return false, errors.New("query unavailable")
			}
			return true, nil
		},
		terminate:     func() error { return nil },
		wait:          func() { waited = true },
		sleep:         func(delay time.Duration) { sleeps = append(sleeps, delay) },
		processID:     42,
		boundary:      "job_object",
		inspectAction: "query_job",
		stopAction:    "terminate_job",
	}.settle(discardAssistedChildLogger())

	if !waited || len(sleeps) != 9 {
		t.Fatalf("cleanup completion: waited=%v sleeps=%v", waited, sleeps)
	}
	for _, delay := range sleeps {
		if delay > assistedChildCleanupRetryMax {
			t.Fatalf("cleanup backoff %s exceeds maximum %s", delay, assistedChildCleanupRetryMax)
		}
	}
	if sleeps[len(sleeps)-1] != assistedChildCleanupRetryMax {
		t.Fatalf("cleanup backoff ended at %s, want cap %s", sleeps[len(sleeps)-1], assistedChildCleanupRetryMax)
	}
}

type assistedChildRunResult struct {
	outcome assistedChildOutcome
	err     error
}

func assistedChildHelperCommand(mode, pidFile string, stubborn bool) *exec.Cmd {
	stubbornValue := "0"
	if stubborn {
		stubbornValue = "1"
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAssistedChildProcessHelper$")
	cmd.Env = append(
		os.Environ(),
		assistedChildHelperMode+"="+mode,
		assistedChildHelperPIDFile+"="+pidFile,
		assistedChildHelperStubborn+"="+stubbornValue,
	)
	return cmd
}

func discardAssistedChildLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func readAssistedChildPID(t *testing.T, path string) int {
	t.Helper()
	if err := waitForAssistedChildPIDFile(path, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read descendant pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("descendant pid = %q, err=%v", raw, err)
	}
	return pid
}

func waitForAssistedChildPIDFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect descendant pid file: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for descendant pid file %s", path)
}

func awaitAssistedChildOutcome(t *testing.T, outcomes <-chan assistedChildRunResult) assistedChildRunResult {
	t.Helper()
	select {
	case result := <-outcomes:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for assisted child cleanup")
		return assistedChildRunResult{}
	}
}

func waitForAssistedChildProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !platformAssistedChildProcessAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
