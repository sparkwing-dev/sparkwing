//go:build !windows

package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOwnHoldsAGroupUntilEveryReleaseRuns(t *testing.T) {
	first := Own(424242)
	second := Own(424242)
	t.Cleanup(func() { first(); second() })
	if !slices.Contains(Owned(), 424242) {
		t.Fatal("an owned group is not listed")
	}
	first()
	first()
	if !slices.Contains(Owned(), 424242) {
		t.Fatal("one release of a twice-owned group dropped it")
	}
	second()
	if slices.Contains(Owned(), 424242) {
		t.Fatal("a fully released group is still listed")
	}
}

func TestOwnRefusesInitAndInvalidGroups(t *testing.T) {
	for _, group := range []int{-1, 0, 1} {
		Own(group)()
		if slices.Contains(Owned(), group) {
			t.Fatalf("group %d was recorded", group)
		}
	}
}

func TestKillOwnedEndsAnOwnedSession(t *testing.T) {
	cmd, pid := startSessionDescendant(t)
	release := Own(pid)
	defer release()

	KillOwned()

	waitForSignalExit(t, cmd, syscall.SIGKILL)
}

func TestForwardTerminationReapsOwnedSessionsBeforeTheProcessDies(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	ownedPID := filepath.Join(dir, "owned")
	owner := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	owner.Env = append(os.Environ(), helperMode+"=owner", procgroupReadyEnv+"="+ready, procgroupOwnedPID+"="+ownedPID)
	owner.Stderr = os.Stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill() })
	waitForHelperFile(t, ready)
	descendant := readHelperPID(t, ownedPID)
	t.Cleanup(func() { _ = syscall.Kill(-descendant, syscall.SIGKILL) })

	if err := owner.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	waitForSignalExit(t, owner, syscall.SIGTERM)
	waitForProcessGone(t, descendant)
}

func startOwnedSessionDescendant(pidFile string) error {
	child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	child.Env = append(os.Environ(), helperMode+"=descendant", procgroupReadyEnv+"=")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return fmt.Errorf("start owned descendant: %w", err)
	}
	Own(child.Process.Pid)
	return os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
}

func startSessionDescendant(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=descendant", procgroupReadyEnv+"=")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd, cmd.Process.Pid
}

func waitForSignalExit(t *testing.T, cmd *exec.Cmd, want syscall.Signal) {
	t.Helper()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatalf("process %d did not exit after %s", cmd.Process.Pid, want)
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != want {
		t.Fatalf("process %d ended with %v, want death by %s", cmd.Process.Pid, cmd.ProcessState, want)
	}
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		// safety: the owner that would reap it is dead, so a killed descendant
		// lingers as a zombie until init collects it.
		if state, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); readErr == nil && processTerminated(statField(string(state))) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d outlived the owner that started it", pid)
}

func statField(stat string) string {
	idx := strings.LastIndexByte(stat, ')')
	if idx < 0 || idx+2 >= len(stat) {
		return ""
	}
	fields := strings.Fields(stat[idx+2:])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func waitForHelperFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper never wrote %s", path)
}

func readHelperPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}
