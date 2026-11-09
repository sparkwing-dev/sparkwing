package nodehost

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// safety: covers every class appendBatchArgument quotes or rewrites.
var windowsBatchArguments = []string{
	"plain", "with spaces", "", "main&ver&rem", "%SW_BATCH_FIXTURE%", "100%", "!SW_BATCH_FIXTURE!", "x|ver", "x<file", "x>file",
	"x^y", `x"y`, `"q"`, `a"&ver&"b`, "(x)", "x)y", "--flag=value", `C:\dir\file`, `trailing\`, `two\\`, `a\"b`,
	"tab\there", "héllo", "日本語",
}

// safety: forwarding %* to node.exe, as npm.cmd does, proves cmd.exe hands the original argv
// to a program that parses its command line with the Microsoft C runtime rules.
//
//sparkwing:critical
func TestWindowsHostCommandBatchArguments(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("batch argument round trip requires node on PATH: %v", err)
	}
	dir := t.TempDir()
	forward := []byte("@\"" + node + "\" -e \"process.stdout.write('ARGV:'+JSON.stringify(process.argv.slice(1)))\" -- %*\r\n")
	for _, extension := range []string{".cmd", ".BAT", ".CmD. "} {
		tool := filepath.Join(dir, "batch"+extension)
		if err := os.WriteFile(strings.TrimRight(tool, " ."), forward, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, arg := range windowsBatchArguments {
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
				got, ok := batchHelperArgv(output.String())
				if done.StartError != "" || done.ExitCode != 0 || !ok || !slices.Equal(got, []string{"safe", arg}) {
					t.Fatalf("batch argv round trip: %+v, got %q, output=%q", done, got, output.String())
				}
			})
		}
		for _, arg := range []string{"x\ry", "x\ny"} {
			req := nodeproto.ExecRequest{ID: "batch", Argv: []string{tool, "safe", arg}, Dir: dir, Env: os.Environ(), Capture: true}
			var output strings.Builder
			done, session := runHostCommand(req, nil, nil, nil, func(_ string, chunk []byte, _ bool) error {
				output.Write(chunk)
				return nil
			}, nil)
			if session != nil || output.Len() != 0 || done.ExitCode != -1 || done.NotFound || !strings.Contains(done.StartError, "argv[2]") || !strings.Contains(done.StartError, "non-batch executable") || strings.Contains(done.StartError, arg) {
				t.Fatalf("%s: unescapable batch argument %q: %+v, output=%q", extension, arg, done, output.String())
			}
		}
	}
}

func batchHelperArgv(output string) ([]string, bool) {
	for line := range strings.Lines(output) {
		if encoded, ok := strings.CutPrefix(strings.TrimSpace(line), "ARGV:"); ok {
			var argv []string
			return argv, json.Unmarshal([]byte(encoded), &argv) == nil
		}
	}
	return nil, false
}

//sparkwing:critical
func TestWindowsHostCommandCleanupBatchArguments(t *testing.T) {
	for _, extension := range []string{".cmd", ".bat"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "npm"+extension), []byte("@echo off\r\necho CLEANUP-RAN\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, arg := range []string{"plain", "main&ver&rem", "%SW_BATCH_FIXTURE%", "x\ny"} {
			t.Run(fmt.Sprintf("%s/%q", extension, arg), func(t *testing.T) {
				cleanups := &ProcessCleanups{}
				mustRegisterCleanup(t, cleanups, "npm", "safe", arg)
				var log strings.Builder
				env := cleanupProcessEnv(t, append(os.Environ(), "PATH="+dir, "SW_BATCH_FIXTURE=fixture-only")...)
				err := cleanups.run(cleanupContext(t), CleanupScope{Dir: dir, Env: env, Log: &log})
				if arg != "x\ny" {
					if err != nil || !strings.Contains(log.String(), "CLEANUP-RAN") {
						t.Fatalf("escaped cleanup: %v, log=%q", err, log.String())
					}
				} else if err == nil || !strings.Contains(err.Error(), "argv[2]") || !strings.Contains(log.String(), "non-batch executable") || strings.Contains(log.String(), "CLEANUP-RAN") || strings.Contains(log.String(), arg) {
					t.Fatalf("unescapable cleanup: %v, log=%q", err, log.String())
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
		for _, arg := range []string{"plain", "main&ver&rem", "x\ry"} {
			t.Run(fmt.Sprintf("%s/%q", name, arg), func(t *testing.T) {
				cleanups := &ProcessCleanups{}
				mustRegisterCleanup(t, cleanups, name, "safe", arg)
				var log strings.Builder
				env := cleanupProcessEnv(t, append(os.Environ(), "PATH=bin", "PATHEXT=.cmd")...)
				err := cleanups.run(cleanupContext(t), CleanupScope{Dir: dir, Env: env, Log: &log})
				if arg != "x\ry" {
					if err != nil || !strings.Contains(log.String(), "CLEANUP-RAN") {
						t.Fatalf("resolved cleanup: %v, log=%q", err, log.String())
					}
				} else if err == nil || !strings.Contains(err.Error(), "argv[2]") || !strings.Contains(log.String(), "non-batch executable") || strings.Contains(log.String(), "CLEANUP-RAN") || strings.Contains(log.String(), arg) {
					t.Fatalf("unescapable extensionless cleanup: %v, log=%q", err, log.String())
				}
			})
		}
	}
}

//sparkwing:critical
func TestWindowsHostCommandFinalBatchArgv(t *testing.T) {
	for _, folder := range []string{"bin", "Program Files (x86)", "Program Files (x86) & Tools", "Tools 100%", "Tools ^ x"} {
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
		if strings.ContainsAny(folder, "%^") {
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
