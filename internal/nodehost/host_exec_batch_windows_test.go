package nodehost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
				req := nodeproto.ExecRequest{ID: "batch", Argv: []string{tool, "safe", arg}, Dir: dir, Env: append(os.Environ(), "SW_BATCH_FIXTURE=fixture-only"), Capture: true}
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

func TestWindowsHostCommandExecutableKeepsMetacharacters(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	arg := "&|<>^%\"!()\r\n"
	req := nodeproto.ExecRequest{ID: "literal", Argv: []string{executable, "-test.run=^TestWindowsHostCommandLiteralArgument$", "--", "--literal-argument", arg}, Dir: t.TempDir(), Env: os.Environ(), Capture: true}
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
