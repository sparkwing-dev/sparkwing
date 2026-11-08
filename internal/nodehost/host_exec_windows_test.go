//go:build windows

package nodehost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func windowsHostRequest(t *testing.T, mode string) nodeproto.ExecRequest {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return nodeproto.ExecRequest{ID: "windows", Argv: []string{executable, "-test.run=^TestWindowsHostCommandHelper$", "--", "--host-command", mode}, Dir: t.TempDir(), Env: os.Environ()}
}

func TestWindowsHostCommandHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--host-command" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "tree" || mode == "branch" || mode == "background" {
		childMode := "hold"
		if mode == "tree" {
			childMode = "branch"
		}
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsHostCommandHelper$", "--", "--host-command", childMode)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		var readyR, readyW *os.File
		if mode == "background" {
			var err error
			readyR, readyW, err = os.Pipe()
			if err != nil {
				os.Exit(13)
			}
			child.Stderr = readyW
		}
		if err := child.Start(); err != nil {
			os.Exit(10)
		}
		if mode == "background" {
			_ = readyW.Close()
			if _, err := io.ReadFull(readyR, make([]byte, 1)); err != nil {
				os.Exit(14)
			}
			os.Exit(0)
		}
	}
	if mode == "output" {
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 128<<10))
		os.Exit(0)
	}
	_, _ = fmt.Fprintf(os.Stdout, "%d\n", os.Getpid())
	_, _ = os.Stderr.WriteString(".")
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		os.Exit(11)
	}
	_, _ = windows.WaitForSingleObject(event, windows.INFINITE)
	os.Exit(12)
}

func waitWindowsProcess(t *testing.T, handle windows.Handle) {
	t.Helper()
	state, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("process survived command cleanup: state=%d, err=%v", state, err)
	}
}

func TestWindowsHostCommandCancellationKillsTree(t *testing.T) {
	for _, end := range []string{"cancel", "session"} {
		t.Run(end, func(t *testing.T) {
			req := windowsHostRequest(t, "tree")
			kill, closed := make(chan struct{}), make(chan struct{})
			ready := make(chan []windows.Handle, 1)
			var output string
			var handles []windows.Handle
			sink := func(stream string, chunk []byte, _ bool) error {
				if stream != "stdout" {
					return nil
				}
				output += string(chunk)
				for strings.Contains(output, "\n") {
					line, rest, _ := strings.Cut(output, "\n")
					output = rest
					pid, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
					if err != nil {
						return err
					}
					handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
					if err != nil {
						return err
					}
					handles = append(handles, handle)
					if len(handles) == 3 {
						ready <- handles
					}
				}
				return nil
			}
			finished := make(chan nodeproto.ExecDone, 1)
			go func() {
				done, session := runHostCommand(req, nil, kill, closed, sink)
				if session != nil {
					_ = endCommandSessions([]*commandSession{session})
				}
				finished <- done
			}()
			defer func() {
				select {
				case <-kill:
				default:
					close(kill)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			select {
			case handles := <-ready:
				defer func() {
					for _, handle := range handles {
						_ = windows.CloseHandle(handle)
					}
				}()
				if end == "cancel" {
					close(kill)
				} else {
					close(closed)
				}
				select {
				case done := <-finished:
					if done.StartError != "" || !done.Signaled || done.ExitCode == 0 {
						t.Fatalf("cancelled command = %+v", done)
					}
				case <-ctx.Done():
					t.Fatal("cancelled command did not return")
				}
				for _, handle := range handles {
					waitWindowsProcess(t, handle)
				}
			case done := <-finished:
				t.Fatalf("command exited before the tree was ready: %+v", done)
			case <-ctx.Done():
				t.Fatal("command tree did not become ready")
			}
		})
	}
}

func TestWindowsHostCommandBackgroundDrainAndClose(t *testing.T) {
	req := windowsHostRequest(t, "background")
	// safety: the background child inherits stdin without reading it, so WaitDelay must join the copier.
	req.Stdin = bytes.Repeat([]byte("x"), 256<<10)
	var output strings.Builder
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done, session := runHostCommand(req, nil, ctx.Done(), ctx.Done(), func(stream string, chunk []byte, _ bool) error {
		if stream == "stdout" {
			output.Write(chunk)
		}
		return nil
	})
	if session == nil {
		t.Fatalf("background command did not start: %+v", done)
	}
	defer func() { _ = endCommandSessions([]*commandSession{session}) }()
	if done.StartError != "" || done.ExitCode != 0 || done.Signaled || ctx.Err() != nil {
		t.Fatalf("background command = %+v, context=%v", done, ctx.Err())
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(output.String()), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if state, err := windows.WaitForSingleObject(handle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("background child exited before session close: state=%d, err=%v", state, err)
	}
	if err := endCommandSessions([]*commandSession{session}); err != nil {
		t.Fatal(err)
	}
	// safety: do not close a handle twice; Windows can recycle it for an unrelated resource.
	session.job = nil
	waitWindowsProcess(t, handle)
}

func TestWindowsHostCommandKeepsBufferedOutputPastSlowSink(t *testing.T) {
	req := windowsHostRequest(t, "output")
	var got int
	done, session := runHostCommand(req, nil, make(chan struct{}), make(chan struct{}), func(_ string, chunk []byte, _ bool) error {
		if len(chunk) != 0 {
			got += len(chunk)
			//nolint:staticcheck // Keep this wait matched to sleepcheck's approved real-pipe boundary.
			select {
			// sleepcheck:external-boundary real OS pipe deadline; synctest cannot model the sink mutex
			case <-time.After(2 * commandDrainGrace):
			}
		}
		return nil
	})
	if session != nil {
		if err := endCommandSessions([]*commandSession{session}); err != nil {
			t.Fatal(err)
		}
	}
	if done.ExitCode != 0 || done.StartError != "" || got != 128<<10 {
		t.Fatalf("captured %d of 131072 bytes: %+v", got, done)
	}
}

func TestWindowsHostCommandNaturalFailureIsNotCancellation(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 7")
	session, err := startHostCommand(cmd, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = endCommandSessions([]*commandSession{session}) }()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	waitWindowsProcess(t, handle)
	session.kill()
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 7 || session.signaled(cmd) {
		t.Fatalf("natural failure attributed to cancellation: %v", err)
	}
}

func TestWindowsHostCommandLongBashProgram(t *testing.T) {
	shell, err := exec.LookPath("bash.exe")
	if err != nil {
		t.Skip("Git Bash is unavailable")
	}
	for _, program := range []string{`read -r value; printf '%s' "$value"; bash -c 'test -z "${SPARKWING_BASH_PROGRAM+x}"'`, strings.Repeat(":;", 6000) + `printf 'input with spaces'`} {
		req := nodeproto.ExecRequest{ID: "bash", Argv: []string{shell, "-c", program}, Dir: t.TempDir(), Env: append(os.Environ(), "SPARKWING_BASH_PROGRAM=exit 42"), Stdin: []byte("input with spaces\n"), Capture: true}
		var output strings.Builder
		done, session := runHostCommand(req, nil, make(chan struct{}), make(chan struct{}), func(stream string, chunk []byte, _ bool) error {
			if stream == "stdout" {
				output.Write(chunk)
			}
			return nil
		})
		if session != nil {
			if err := endCommandSessions([]*commandSession{session}); err != nil {
				t.Fatal(err)
			}
		}
		if done.StartError != "" || done.ExitCode != 0 || output.String() != "input with spaces" {
			t.Fatalf("Bash result: %+v, output=%q", done, output.String())
		}
	}
}

func TestWindowsHostCommandRejectsOversizedBashProgram(t *testing.T) {
	cmd := exec.Command(filepath.Join(t.TempDir(), "bash.exe"), "-c", strings.Repeat("x", 32768))
	configureHostCommand(cmd)
	if cmd.Err == nil || !strings.Contains(cmd.Err.Error(), "exceeds environment limit") {
		t.Fatalf("oversized Bash program error = %v", cmd.Err)
	}
}
