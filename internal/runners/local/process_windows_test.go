//go:build windows

package local

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

func TestWindowsNodeHelper(t *testing.T) {
	mode := os.Getenv("SPARKWING_WINDOWS_NODE_TEST")
	if mode == "" {
		return
	}
	path := os.Getenv("SPARKWING_WINDOWS_NODE_PID")
	switch mode {
	case "leaf":
		ready, err := windowsNodeReadyEvent(path)
		if err != nil {
			os.Exit(13)
		}
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(10)
		}
		if err := windows.SetEvent(ready); err != nil {
			os.Exit(14)
		}
		parkWindowsNodeHelper()
	case "tree", "detached-tree":
		child := windowsNodeHelper("leaf", path)
		if err := child.Start(); err != nil {
			os.Exit(11)
		}
		if mode == "detached-tree" {
			if waitWindowsPIDFile(path) == 0 {
				os.Exit(15)
			}
			os.Exit(0)
		}
		_ = child.Wait()
	case "owner":
		child := windowsNodeHelper("tree", path)
		_, err := startNodeProcess(child, slog.Default())
		if err != nil {
			os.Exit(12)
		}
		parkWindowsNodeHelper()
	case "failure":
		os.Exit(7)
	}
	os.Exit(0)
}

func windowsNodeHelper(mode, path string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsNodeHelper$")
	cmd.Env = append(withoutEnv(os.Environ(), []string{"SPARKWING_WINDOWS_NODE_TEST", "SPARKWING_WINDOWS_NODE_PID"}),
		"SPARKWING_WINDOWS_NODE_TEST="+mode, "SPARKWING_WINDOWS_NODE_PID="+path)
	return cmd
}

func windowsNodeReadyEvent(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Local\sparkwing-node-ready-%x`, sha256.Sum256([]byte(path))))
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateEvent(nil, 1, 0, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		err = nil
	}
	return handle, err
}

func retainWindowsNodeReadyEvent(t *testing.T, path string) {
	t.Helper()
	handle, err := windowsNodeReadyEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(handle) })
}

func parkWindowsNodeHelper() {
	handle, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		os.Exit(16)
	}
	defer windows.CloseHandle(handle)
	_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
	os.Exit(17)
}

func waitWindowsPIDFile(path string) int {
	handle, err := windowsNodeReadyEvent(path)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 8000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return 0
	}
	return pid
}

func requireWindowsPID(t *testing.T, path string) int {
	t.Helper()
	pid := waitWindowsPIDFile(path)
	if pid == 0 {
		t.Fatal("child did not become ready")
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
	return pid
}

func requireWindowsProcessGone(t *testing.T, pid int) {
	t.Helper()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 8000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("process %d survived cleanup: %v, %v", pid, state, err)
	}
}

func TestWindowsNodeTerminationCleansDescendants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leaf.pid")
	retainWindowsNodeReadyEvent(t, path)
	p, err := startNodeProcess(windowsNodeHelper("tree", path), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = p.Terminate(ctx, 0) })
	pid := requireWindowsPID(t, path)
	_ = p.Terminate(ctx, 0)
	requireWindowsProcessGone(t, pid)
	select {
	case <-ctx.Done():
		t.Fatal("cleanup exceeded deadline")
	default:
	}
}

func TestWindowsNodesStartedAtSameTimestampKeepIndependentJobs(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first.pid")
	secondPath := filepath.Join(t.TempDir(), "second.pid")
	retainWindowsNodeReadyEvent(t, firstPath)
	retainWindowsNodeReadyEvent(t, secondPath)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	timestamp := time.Now().UnixNano()
	first, err := startNodeProcessAt(windowsNodeHelper("tree", firstPath), logger, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = first.Terminate(ctx, 0)
	})
	second, err := startNodeProcessAt(windowsNodeHelper("tree", secondPath), logger, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = second.Terminate(ctx, 0)
	})
	if first.(*windowsNodeProcess).job.Name == second.(*windowsNodeProcess).job.Name {
		t.Fatal("nodes started at the same timestamp share a job")
	}
	firstPID := requireWindowsPID(t, firstPath)
	secondPID := requireWindowsPID(t, secondPath)
	secondProcess, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(secondPID))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(secondProcess)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_ = first.Terminate(ctx, 0)
	requireWindowsProcessGone(t, firstPID)
	state, err := windows.WaitForSingleObject(secondProcess, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("terminating the first node also ended the second: wait status %d, err %v", state, err)
	}
	_ = second.Terminate(ctx, 0)
	requireWindowsProcessGone(t, secondPID)
}

func TestWindowsNodeNormalExitCleansDetachedDescendant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leaf.pid")
	retainWindowsNodeReadyEvent(t, path)
	p, err := startNodeProcess(windowsNodeHelper("detached-tree", path), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = p.Terminate(ctx, 0) })
	pid := requireWindowsPID(t, path)
	if err := p.Finish(ctx, 0); err != nil {
		t.Fatal(err)
	}
	requireWindowsProcessGone(t, pid)
}

func TestWindowsNodeDispatcherDeathClosesJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leaf.pid")
	retainWindowsNodeReadyEvent(t, path)
	owner := windowsNodeHelper("owner", path)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })
	pid := requireWindowsPID(t, path)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	requireWindowsProcessGone(t, pid)
}

func TestWindowsNodePreservesExitStatus(t *testing.T) {
	p, err := startNodeProcess(windowsNodeHelper("failure", ""), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = p.Terminate(ctx, 0) })
	var exit *exec.ExitError
	if err := p.Finish(ctx, 0); !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit = %v, want 7", err)
	}
}

func TestWindowsNodeDoesNotClaimUnixDescriptor(t *testing.T) {
	cmd := exec.Command("unused")
	cmd.Env = childEnv(context.Background(), []string{ParentLivenessFDEnv + "=3"}, Config{}, runner.Request{})
	releaseChild, releaseParent, err := prepareNodeLiveness(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseChild()
	defer releaseParent()
	if len(cmd.ExtraFiles) != 0 {
		t.Fatal("Windows must not pass Unix ExtraFiles")
	}
	if _, found := lastValue(cmd.Env, ParentLivenessFDEnv); found {
		t.Fatal("Windows child inherited fd 3 authority")
	}
}
