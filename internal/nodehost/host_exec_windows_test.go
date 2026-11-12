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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func windowsHostRequest(t *testing.T, mode string) nodewire.ExecRequest {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return nodewire.ExecRequest{ID: "windows", Argv: []string{executable, "-test.run=^TestWindowsHostCommandHelper$", "--", "--host-command", mode}, Dir: t.TempDir(), Env: os.Environ()}
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
			finished := make(chan nodewire.ExecDone, 1)
			go func() {
				done, session := runHostCommand(req, nil, kill, closed, sink, nil)
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
	}, nil)
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
	}, nil)
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
	if err := session.kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 7 || session.signaled(cmd) {
		t.Fatalf("natural failure attributed to cancellation: %v", err)
	}
}

func windowsGitBash(t *testing.T) string {
	t.Helper()
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs")} {
		if root == "" {
			continue
		}
		for _, dir := range []string{"bin", "usr/bin"} {
			path := filepath.Join(root, "Git", dir, "bash.exe")
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path
			}
		}
	}
	t.Skip("Git Bash is unavailable in standard installation paths")
	return ""
}

func TestWindowsHostCommandAcceptsGitBash(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "Git", "bin", "bash.exe")
	if err := os.MkdirAll(filepath.Dir(tool), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := procenv.SDKCommand(os.Environ(), dir, tool, "-c", "printf expected")
	configureHostCommand(cmd)
	if cmd.Err != nil || !strings.Contains(strings.Join(cmd.Env, "\n"), "SPARKWING_BASH_PROGRAM=printf expected") {
		t.Fatalf("Git Bash command was not configured: %v, %v", cmd.Err, cmd.Args)
	}
}

func TestWindowsHostCommandLongBashProgram(t *testing.T) {
	shell := windowsGitBash(t)
	for _, program := range []string{`read -r value; printf '%s' "$value"; bash -c 'test -z "${SPARKWING_BASH_PROGRAM+x}"'`, strings.Repeat(":\n", 6000) + `printf 'input with spaces'`} {
		req := nodewire.ExecRequest{ID: "bash", Argv: []string{shell, "-c", program}, Dir: t.TempDir(), Env: append(os.Environ(), "SPARKWING_BASH_PROGRAM=exit 42"), Stdin: []byte("input with spaces\n"), Capture: true}
		var output, diagnostic strings.Builder
		done, session := runHostCommand(req, nil, make(chan struct{}), make(chan struct{}), func(stream string, chunk []byte, _ bool) error {
			if stream == "stdout" {
				output.Write(chunk)
			} else {
				diagnostic.Write(chunk)
			}
			return nil
		}, nil)
		if session != nil {
			if err := endCommandSessions([]*commandSession{session}); err != nil {
				t.Fatal(err)
			}
		}
		if done.StartError != "" || done.ExitCode != 0 || output.String() != "input with spaces" {
			t.Fatalf("Bash %q result: %+v, output=%q stderr=%q", shell, done, output.String(), diagnostic.String())
		}
	}
}

func TestWindowsHostCommandRejectsOversizedBashProgram(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "bash.exe")
	if err := os.WriteFile(tool, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := procenv.SDKCommand(os.Environ(), dir, tool, "-c", strings.Repeat("x", 32768))
	configureHostCommand(cmd)
	if cmd.Err == nil || !strings.Contains(cmd.Err.Error(), "write the program to a script file") {
		t.Fatalf("oversized Bash program error = %v", cmd.Err)
	}
}

//sparkwing:critical
func TestWindowsHostCommandRejectsWSLBash(t *testing.T) {
	root := t.TempDir()
	system32 := filepath.Join(root, "System32")
	if err := os.Mkdir(system32, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(system32, "bash.exe")
	if err := os.WriteFile(tool, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SystemRoot", root)
	for _, name := range []string{tool, strings.ToUpper(tool), `System32\bash.exe`} {
		req := nodewire.ExecRequest{ID: "wsl", Argv: []string{name, "-c", "printf unexpected"}, Dir: root, Env: os.Environ(), Capture: true}
		done, session := runHostCommand(req, nil, nil, nil, func(string, []byte, bool) error { return nil }, nil)
		if session != nil {
			if err := endCommandSessions([]*commandSession{session}); err != nil {
				t.Fatal(err)
			}
		}
		if session != nil || !strings.Contains(done.StartError, "WSL bash.exe is not supported") || !strings.Contains(done.StartError, "install Git Bash and set the Bash path") {
			t.Fatalf("WSL launcher not refused: %+v", done)
		}
	}
}

//sparkwing:critical
func TestWindowsHostCommandBashEmptyProgram(t *testing.T) {
	shell := windowsGitBash(t)
	for _, missing := range []bool{false, true} {
		cmd := procenv.SDKCommand(os.Environ(), t.TempDir(), shell, "-c", "")
		configureHostCommand(cmd)
		if missing {
			cmd.Env = slices.DeleteFunc(cmd.Env, func(entry string) bool {
				key, _, _ := strings.Cut(entry, "=")
				return strings.EqualFold(key, "SPARKWING_BASH_PROGRAM")
			})
		}
		output, err := cmd.CombinedOutput()
		if missing {
			if err == nil || !strings.Contains(string(output), "host Bash program is missing") {
				t.Fatalf("missing Bash program succeeded: %v, output=%q", err, output)
			}
		} else if err != nil || len(output) != 0 {
			t.Fatalf("empty Bash program failed: %v, output=%q", err, output)
		}
	}
}

func TestWindowsCapturedEnvironmentIgnoresDriveMetadata(t *testing.T) {
	snapshots, err := NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seal, err := snapshots.Capture(t.Context(), []string{`=C:=C:\work`, "CUSTOM=value"})
	if err != nil {
		t.Fatal(err)
	}
	values, err := snapshots.Load(t.Context(), seal)
	if err != nil || len(values) != 1 || values[0] != "CUSTOM=value" {
		t.Fatalf("values=%v err=%v", values, err)
	}
}

func TestWindowsHostCommandFiltersEngineCredentials(t *testing.T) {
	t.Setenv("SPARKWING_NODE_TOKEN", "fixture-only")
	for _, explicit := range []bool{false, true} {
		req := nodewire.ExecRequest{ID: "credentials", Argv: []string{"cmd", "/c", "if defined SPARKWING_NODE_TOKEN (exit 23) else (echo filtered)"}, Dir: t.TempDir(), Env: os.Environ(), Capture: true}
		if explicit {
			req.Env = append(req.Env, "SPARKWING_NODE_TOKEN=fixture-override")
		}
		var output strings.Builder
		done, session := runHostCommand(req, nil, make(chan struct{}), make(chan struct{}), func(stream string, chunk []byte, _ bool) error {
			if stream == "stdout" {
				output.Write(chunk)
			}
			return nil
		}, nil)
		if session != nil {
			if err := endCommandSessions([]*commandSession{session}); err != nil {
				t.Fatal(err)
			}
		}
		if done.StartError != "" || done.ExitCode != 0 || strings.TrimSpace(output.String()) != "filtered" {
			t.Fatalf("explicit=%v: credential filtering result=%+v, output=%q", explicit, done, output.String())
		}
	}
}

func TestWindowsHostCommandUsesCommandPath(t *testing.T) {
	directory := t.TempDir()
	source, err := exec.LookPath("cmd")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "host-tool.exe"), body, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path string }{
		{"host-tool", directory},
		{filepath.Join(directory, "host-tool"), t.TempDir()},
		{`.\host-tool`, t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := nodewire.ExecRequest{ID: "lookup", Argv: []string{tc.name, "/c", "echo resolved"}, Dir: directory, Env: append(os.Environ(), "PATH="+tc.path, "PATHEXT=.EXE"), Capture: true}
			var output strings.Builder
			done, session := runHostCommand(req, nil, make(chan struct{}), make(chan struct{}), func(stream string, chunk []byte, _ bool) error {
				if stream == "stdout" {
					output.Write(chunk)
				}
				return nil
			}, nil)
			if session != nil {
				if err := endCommandSessions([]*commandSession{session}); err != nil {
					t.Fatal(err)
				}
			}
			if done.StartError != "" || done.ExitCode != 0 || strings.TrimSpace(output.String()) != "resolved" {
				t.Fatalf("command PATH result=%+v, output=%q", done, output.String())
			}
		})
	}
}
