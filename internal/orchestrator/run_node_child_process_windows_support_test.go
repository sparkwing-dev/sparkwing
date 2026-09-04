//go:build windows

package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func platformIgnoreAssistedChildTermination() {}

func platformConfigureAssistedChildDescendant(*exec.Cmd) {}

func platformAttemptAssistedChildBreakaway() error {
	child := exec.Command(os.Args[0], "-test.run=^TestAssistedChildProcessHelper$")
	child.Env = append(
		os.Environ(),
		assistedChildHelperMode+"=mark",
		assistedChildHelperMarker+"="+os.Getenv(assistedChildHelperMarker)+".escaped",
	)
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := child.Start(); err != nil {
		return err
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	return nil
}

func platformAssistedChildProcessAlive(pid int) bool {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
		return true
	}
	return exitCode == windowsStillActive
}

func platformKillAssistedChildTestProcess(pid int) {
	if pid <= 1 {
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
}

func TestAssistedChildJobRejectsBreakaway(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "breakaway")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAssistedChildProcessHelper$")
	cmd.Env = append(
		os.Environ(),
		assistedChildHelperMode+"=breakaway",
		assistedChildHelperMarker+"="+marker,
	)
	outcome, err := runAssistedChildProcess(context.Background(), cmd, discardAssistedChildLogger())
	if err != nil || outcome.cancelCause != nil || outcome.waitErr != nil {
		t.Fatalf("breakaway probe result = %+v err=%v", outcome, err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read breakaway result: %v", err)
	}
	if string(raw) != "denied" {
		t.Fatalf("breakaway result = %q, want denied", raw)
	}
}

func TestAssistedChildUnexpectedSuspendCountReapsBeforeBodyRuns(t *testing.T) {
	previousResume := resumeAssistedChildThread
	resumeAssistedChildThread = func(windows.Handle) (uint32, error) {
		return 2, nil
	}
	t.Cleanup(func() { resumeAssistedChildThread = previousResume })

	marker := filepath.Join(t.TempDir(), "body-started")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAssistedChildProcessHelper$")
	cmd.Env = append(
		os.Environ(),
		assistedChildHelperMode+"=mark",
		assistedChildHelperMarker+"="+marker,
	)
	outcome, err := runAssistedChildProcess(context.Background(), cmd, discardAssistedChildLogger())
	if err == nil || outcome.cancelCause != nil || outcome.waitErr != nil {
		t.Fatalf("unexpected suspend count result = %+v err=%v", outcome, err)
	}
	if !strings.Contains(err.Error(), "unexpected previous suspend count 2") {
		t.Fatalf("unexpected suspend count error = %v", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("unexpected suspend count did not reap process: %+v", cmd.ProcessState)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("body ran before suspend-count rejection: %v", err)
	}
}
