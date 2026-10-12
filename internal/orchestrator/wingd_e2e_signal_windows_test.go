//go:build windows

package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func runIsolatedInterruptTest(t *testing.T) bool {
	t.Helper()
	if os.Getenv("SPARKWING_TEST_PRIVATE_INTERRUPT_CONSOLE") == "1" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunLocal_SIGINTFinalizesRunAsCancelledAndReleasesLease$", "-test.count=1", "-test.timeout=40s")
	cmd.Env = append(os.Environ(), "SPARKWING_TEST_PRIVATE_INTERRUPT_CONSOLE=1")
	// bug: console events must not reach the test launcher or unrelated sibling processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated native console interrupt: %v\n%s", err, output)
	}
	return true
}

func signalSelfInterruptForTest() error {
	// bug: CTRL_C can be inherited as ignored; CTRL_BREAK always reaches Go's interrupt handler.
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, 0)
}
