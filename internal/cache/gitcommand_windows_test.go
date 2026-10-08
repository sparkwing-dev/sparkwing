//go:build windows

package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

var gitReadySequence atomic.Uint64

func TestGitCommandWindowsHelper(t *testing.T) {
	role := os.Getenv("SPARKWING_CACHE_GIT_ROLE")
	if role == "" {
		return
	}
	if role != "child" {
		child := exec.Command(os.Args[0], "-test.run=^TestGitCommandWindowsHelper$")
		child.Env = append(os.Environ(), "SPARKWING_CACHE_GIT_ROLE=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(11)
		}
		if err := os.WriteFile(os.Getenv("SPARKWING_CACHE_GIT_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(12)
		}
		if name := os.Getenv("SPARKWING_CACHE_GIT_READY"); name != "" {
			wide, err := windows.UTF16PtrFromString(name)
			if err != nil {
				os.Exit(15)
			}
			ready, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, wide)
			if err != nil {
				os.Exit(16)
			}
			if err := windows.SetEvent(ready); err != nil {
				os.Exit(17)
			}
			_ = windows.CloseHandle(ready)
		}
		if role == "exit" {
			fmt.Print("stdout")
			fmt.Fprint(os.Stderr, "stderr")
			os.Exit(7)
		}
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		os.Exit(13)
	}
	_, _ = windows.WaitForSingleObject(event, windows.INFINITE)
	os.Exit(14)
}

func TestGitCommandWindowsCancellationEndsDescendant(t *testing.T) {
	bound, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(bound)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "child.pid")
	name := `Local\sparkwing-cache-ready-` + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(gitReadySequence.Add(1), 10)
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := windows.CreateEvent(nil, 1, 0, wide)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ready)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitCommandWindowsHelper$")
	cmd.Env = append(os.Environ(), "SPARKWING_CACHE_GIT_ROLE=wait", "SPARKWING_CACHE_GIT_PID="+marker, "SPARKWING_CACHE_GIT_READY="+name)
	done := make(chan error, 1)
	go func() { _, err := gitCommandOutput(cmd, false); done <- err }()
	if state, err := windows.WaitForSingleObject(ready, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("helper never became ready: state=%d error=%v", state, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-bound.Done():
		t.Fatal("cancelled git command did not finish")
	}
	if state, err := windows.WaitForSingleObject(process, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant remains after cancellation: state=%d error=%v", state, err)
	}
}

func TestGitCommandWindowsNaturalExitCleansDescendant(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(strconv.FormatBool(combined), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			marker := filepath.Join(t.TempDir(), "child.pid")
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitCommandWindowsHelper$")
			cmd.Env = append(os.Environ(), "SPARKWING_CACHE_GIT_ROLE=exit", "SPARKWING_CACHE_GIT_PID="+marker)
			out, err := gitCommandOutput(cmd, combined)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 7 {
				t.Fatalf("natural exit = %v, want exit 7", err)
			}
			if !strings.Contains(string(out), "stdout") || strings.Contains(string(out), "stderr") != combined {
				t.Fatalf("captured output = %q", out)
			}
			if !combined && string(exit.Stderr) != "stderr" {
				t.Fatalf("captured error output = %q", exit.Stderr)
			}
			data, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process)
			if state, err := windows.WaitForSingleObject(process, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("descendant remains after natural exit: state=%d error=%v", state, err)
			}
		})
	}
}

func TestGitCommandWindowsStartFailure(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), filepath.Join(t.TempDir(), "missing.exe"))
	if _, err := gitCommandOutput(cmd, false); err == nil {
		t.Fatal("missing executable succeeded")
	}
}

func TestGitCommandWindowsSuccess(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	out, err := gitCommandOutput(gitCommand(t.Context(), "--version"), false)
	if err != nil || !strings.HasPrefix(string(out), "git version ") {
		t.Fatalf("git --version = %q, error = %v", out, err)
	}
}
