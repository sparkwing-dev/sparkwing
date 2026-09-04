//go:build windows

package releaseasset

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsProbeHelperMode = "SPARKWING_TEST_RELEASE_PROBE_HELPER"
	windowsProbePIDFile    = "SPARKWING_TEST_RELEASE_PROBE_PID_FILE"
)

func TestWindowsProbeProcessHelper(t *testing.T) {
	switch os.Getenv(windowsProbeHelperMode) {
	case "descendant":
		time.Sleep(5 * time.Second)
	case "leader":
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsProbeProcessHelper$")
		child.Env = append(os.Environ(), windowsProbeHelperMode+"=descendant")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv(windowsProbePIDFile), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Skip("probe process helper")
	}
}

func TestWindowsProbeWaitDelayBoundsButDoesNotOwnDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsProbeProcessHelper$")
	cmd.Env = append(os.Environ(), windowsProbeHelperMode+"=leader", windowsProbePIDFile+"="+pidFile)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	cmd.WaitDelay = 100 * time.Millisecond
	started := time.Now()
	err := runProbeProcess(ctx, cmd, cmd.WaitDelay)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("probe error = %v, want exec.ErrWaitDelay", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("probe waited %s for a descendant-owned pipe", elapsed)
	}
	body, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open descendant %d: %v", pid, err)
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("descendant state = %#x, want live WAIT_TIMEOUT", state)
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.WaitForSingleObject(handle, 2_000); err != nil {
		t.Fatal(err)
	}
}
