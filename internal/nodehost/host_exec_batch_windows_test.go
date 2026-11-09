package nodehost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

//sparkwing:critical
func TestWindowsHostCommandBatchArguments(t *testing.T) {
	dir := t.TempDir()
	for _, extension := range []string{".cmd", ".BAT", ".CmD. "} {
		tool := filepath.Join(dir, "batch"+extension)
		if err := os.WriteFile(strings.TrimRight(tool, " ."), []byte("@echo off\r\necho BATCH-RAN\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, arg := range []string{"plain", "with spaces", "main&ver&rem", "%SW_BATCH_FIXTURE%", "!SW_BATCH_FIXTURE!", "x|ver", "x<file", "x>file", "x^y", "x\"y", "x(y", "x)y", "x\ry", "x\ny"} {
			// bug: cmd.exe rejects quoted trailing-dot batch names even with safe arguments.
			if extension == ".CmD. " && arg == "with spaces" {
				continue
			}
			t.Run(fmt.Sprintf("%s/%q", extension, arg), func(t *testing.T) {
				req := nodewire.ExecRequest{ID: "batch", Argv: []string{tool, "safe", arg}, Dir: dir, Env: append(os.Environ(), "SW_BATCH_FIXTURE=fixture-only"), Capture: true}
				if extension == ".cmd" {
					req.Argv[0] = "batch"
					req.Env = append(req.Env, "PATH="+dir, "PATHEXT=.cmd")
				}
				var output strings.Builder
				done, session := runHostCommand(req, nil, nil, nil, func(_ string, chunk []byte, _ bool) error {
					output.Write(chunk)
					return nil
				}, nil)
				if session != nil {
					if err := endCommandSessions([]*commandSession{session}); err != nil {
						t.Fatal(err)
					}
				}
				if arg == "plain" || arg == "with spaces" {
					if done.StartError != "" || done.ExitCode != 0 || !strings.Contains(output.String(), "BATCH-RAN") {
						t.Fatalf("safe batch argument: %+v, output=%q", done, output.String())
					}
				} else if session != nil || output.Len() != 0 || done.ExitCode != -1 || done.NotFound || !strings.Contains(done.StartError, "argv[2]") || !strings.Contains(done.StartError, "non-batch executable") || strings.Contains(done.StartError, arg) {
					t.Fatalf("unsafe batch argument: %+v, output=%q", done, output.String())
				}
			})
		}
	}
}

//sparkwing:critical
func TestWindowsHostCommandCleanupBatchArguments(t *testing.T) {
	for _, extension := range []string{".cmd", ".bat"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "npm"+extension), []byte("@echo off\r\necho CLEANUP-RAN\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, arg := range []string{"plain", "main&ver&rem", "%SW_BATCH_FIXTURE%"} {
			t.Run(fmt.Sprintf("%s/%q", extension, arg), func(t *testing.T) {
				cleanups := &ProcessCleanups{}
				mustRegisterCleanup(t, cleanups, "npm", "safe", arg)
				var log strings.Builder
				env := cleanupProcessEnv(t, append(os.Environ(), "PATH="+dir, "SW_BATCH_FIXTURE=fixture-only")...)
				err := cleanups.run(cleanupContext(t), CleanupScope{Dir: dir, Env: env, Log: &log})
				if arg == "plain" {
					if err != nil || !strings.Contains(log.String(), "CLEANUP-RAN") {
						t.Fatalf("safe cleanup: %v, log=%q", err, log.String())
					}
				} else if err == nil || !strings.Contains(err.Error(), "argv[2]") || !strings.Contains(log.String(), "non-batch executable") || strings.Contains(log.String(), "CLEANUP-RAN") || strings.Contains(log.String(), arg) {
					t.Fatalf("unsafe cleanup: %v, log=%q", err, log.String())
				}
			})
		}
	}
}

//sparkwing:critical
func TestWindowsHostCommandCleanupResolvesBatchPaths(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{dir, bin} {
		if err := os.WriteFile(filepath.Join(folder, "tool.cmd"), []byte("@echo off\r\necho CLEANUP-RAN\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{`.\tool`, `bin\tool`, "tool"} {
		for _, arg := range []string{"plain", "main&ver&rem"} {
			t.Run(name+"/"+arg, func(t *testing.T) {
				cleanups := &ProcessCleanups{}
				mustRegisterCleanup(t, cleanups, name, "safe", arg)
				var log strings.Builder
				env := cleanupProcessEnv(t, append(os.Environ(), "PATH=bin", "PATHEXT=.cmd")...)
				err := cleanups.run(cleanupContext(t), CleanupScope{Dir: dir, Env: env, Log: &log})
				if arg == "plain" {
					if err != nil || !strings.Contains(log.String(), "CLEANUP-RAN") {
						t.Fatalf("resolved cleanup: %v, log=%q", err, log.String())
					}
				} else if err == nil || !strings.Contains(err.Error(), "argv[2]") || !strings.Contains(log.String(), "non-batch executable") || strings.Contains(log.String(), "CLEANUP-RAN") || strings.Contains(log.String(), arg) {
					t.Fatalf("unsafe extensionless cleanup: %v, log=%q", err, log.String())
				}
			})
		}
	}
}

//sparkwing:critical
func TestWindowsHostCommandFinalBatchArgv(t *testing.T) {
	for _, folder := range []string{"bin", "Program Files (x86)", "Program Files (x86) & Tools"} {
		dir := t.TempDir()
		bin := filepath.Join(dir, folder)
		if err := os.Mkdir(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		tool := filepath.Join(bin, "tool.cmd")
		if err := os.WriteFile(tool, []byte("@echo off\r\necho %0\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		req := nodewire.ExecRequest{ID: "argv", Argv: []string{"tool", "plain"}, Dir: dir, Env: append(os.Environ(), "PATH="+bin, "PATHEXT=.cmd"), Capture: true}
		var output strings.Builder
		done, session := runHostCommand(req, nil, nil, nil, func(_ string, data []byte, _ bool) error { output.Write(data); return nil }, nil)
		if session != nil {
			if err := endCommandSessions([]*commandSession{session}); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(folder, "&") {
			if session != nil || output.Len() != 0 || !strings.Contains(done.StartError, "argv[0]") || !strings.Contains(done.StartError, "non-batch executable") {
				t.Fatalf("unsafe resolved program path: %+v, output=%q", done, output.String())
			}
			continue
		}
		if done.StartError != "" || done.ExitCode != 0 || strings.Trim(output.String(), "\" \r\n") != tool {
			t.Fatalf("final argv[0]: %+v, output=%q, want %q", done, output.String(), tool)
		}
	}
}

func TestWindowsHostCommandExecutableKeepsMetacharacters(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	arg := "&|<>^%\"!()\r\n"
	req := nodewire.ExecRequest{ID: "literal", Argv: []string{executable, "-test.run=^TestWindowsHostCommandLiteralArgument$", "--", "--literal-argument", arg}, Dir: t.TempDir(), Env: os.Environ(), Capture: true}
	done, session := runHostCommand(req, nil, nil, nil, func(string, []byte, bool) error { return nil }, nil)
	if session != nil {
		if err := endCommandSessions([]*commandSession{session}); err != nil {
			t.Fatal(err)
		}
	}
	if done.StartError != "" || done.ExitCode != 0 {
		t.Fatalf("literal executable argument: %+v", done)
	}
}

func TestWindowsHostCommandLiteralArgument(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--literal-argument" {
		return
	}
	if os.Args[len(os.Args)-1] != "&|<>^%\"!()\r\n" {
		t.Fatalf("argument changed: %q", os.Args[len(os.Args)-1])
	}
}
