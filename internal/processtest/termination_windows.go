//go:build windows

package processtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// safety: the handshake stays independent of output capture and never reads cmd.Process during Start.
func windowsRun(t *testing.T, mode string, commandContext bool, run func(context.Context, *exec.Cmd) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	input, send, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
	}()
	defer func() {
		if err := send.Close(); err != nil {
			t.Error(err)
		}
	}()
	ready := filepath.Join(t.TempDir(), "ready")
	args := []string{"-test.run=^TestWindowsTerminationChild$", "--", "owned-process-child", ready}
	cmd := procenv.ControlCommand(os.Environ(), os.Args[0], args...)
	if commandContext {
		cmd = procenv.ControlCommandContext(ctx, os.Environ(), os.Args[0], args...)
	}
	cmd.WaitDelay = 50 * time.Millisecond
	cmd.Stdin = input
	ordered := make(chan error, 1)
	go func() {
		defer cancel()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		var pid int
		for pid == 0 {
			raw, e := os.ReadFile(ready)
			if e == nil {
				if parsed, parseErr := strconv.Atoi(string(raw)); parseErr == nil {
					pid = parsed
				}
			}
			if pid != 0 {
				break
			}
			select {
			case <-ctx.Done():
				ordered <- fmt.Errorf("child readiness: %w", ctx.Err())
				return
			case <-ticker.C:
			}
		}
		if strings.HasPrefix(mode, "owner") {
			ordered <- nil
			return
		}
		process, e := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
		if e != nil {
			ordered <- e
			return
		}

		if mode == "exit23" {
			_, e = send.Write([]byte{'x'})
		} else {
			e = windows.TerminateProcess(process, 1)
		}
		if e == nil {
			_, e = windows.WaitForSingleObject(process, windows.INFINITE)
		}
		ordered <- errors.Join(e, windows.CloseHandle(process))
	}()
	result := run(ctx, cmd)
	cancel()
	if fixtureErr := <-ordered; fixtureErr != nil {
		t.Fatalf("native ordering fixture: %v (command: %v)", fixtureErr, result)
	}
	return result
}

// WindowsChild supplies one native child implementation for all process-owner tests.
func WindowsChild(t *testing.T) {
	t.Helper()
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "owned-process-child" {
		return
	}
	if err := os.WriteFile(os.Args[len(os.Args)-1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	os.Exit(23) //nolint:forbidigo // This marker-gated fixture must expose an actual child exit status.
}

// CheckWindowsTermination verifies owner cancellation and preserves prior external status.
func CheckWindowsTermination(t *testing.T, commandContext bool, run func(context.Context, *exec.Cmd) error) {
	t.Helper()
	for _, mode := range []string{"owner", "external", "exit23"} {
		t.Run(mode, func(t *testing.T) {
			for trial := 0; trial < 8; trial++ {
				err := windowsRun(t, mode, commandContext, run)
				var exit *exec.ExitError
				if mode == "owner" {
					if !errors.Is(err, context.Canceled) || errors.As(err, &exit) {
						t.Fatalf("owner termination became a genuine failure: %v", err)
					}
				} else {
					want := 1
					if mode == "exit23" {
						want = 23
					}
					if !errors.As(err, &exit) || exit.ExitCode() != want {
						t.Fatalf("external status lost: %v", err)
					}
				}
			}
		})
	}
}
