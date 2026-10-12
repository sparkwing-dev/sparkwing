//go:build windows

package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAssistedChildNativeConsoleHelper(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_ASSISTED_CONSOLE_HELPER") != "1" {
		return
	}
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "console=%d\nstdin=%s\n", console, input)
	fmt.Fprintln(os.Stderr, "retained stderr")
	os.Exit(7)
}

func TestAssistedChildNativeCommandHasNoConsoleAndPreservesIOAndFlags(t *testing.T) {
	t.Setenv("SPARKWING_TEST_ASSISTED_CONSOLE_HELPER", "1")
	for _, tc := range []struct {
		name    string
		flags   uint32
		console bool
	}{
		{name: "default", flags: windows.CREATE_NEW_PROCESS_GROUP},
		{name: "explicit-console", flags: windows.CREATE_NEW_CONSOLE, console: true},
		{name: "explicit-detached", flags: windows.DETACHED_PROCESS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestAssistedChildNativeConsoleHelper$")
			attrs := &syscall.SysProcAttr{CreationFlags: tc.flags, HideWindow: true}
			cmd.SysProcAttr = attrs
			cmd.Stdin = strings.NewReader("retained stdin")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			outcome, err := runAssistedChildProcess(ctx, cmd, discardAssistedChildLogger())
			var exit *exec.ExitError
			if err != nil || outcome.cancelCause != nil || !errors.As(outcome.waitErr, &exit) || exit.ExitCode() != 7 {
				t.Fatalf("native result = %+v, %v; want exit 7", outcome, err)
			}
			var console uint64
			if _, err := fmt.Sscanf(stdout.String(), "console=%d\n", &console); err != nil || (console != 0) != tc.console {
				t.Fatalf("native console = %d, %v; expected allocated=%t", console, err, tc.console)
			}
			if !strings.HasSuffix(stdout.String(), "\nstdin=retained stdin\n") || stderr.String() != "retained stderr\n" {
				t.Fatalf("native streams stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if cmd.SysProcAttr != attrs || !attrs.HideWindow || attrs.CreationFlags != tc.flags|windows.CREATE_SUSPENDED|windows.CREATE_NO_WINDOW {
				t.Fatalf("process attributes changed: %+v", cmd.SysProcAttr)
			}
		})
	}
}
