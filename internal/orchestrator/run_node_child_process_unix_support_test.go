//go:build !windows

package orchestrator

import (
	"errors"
	"os/exec"
	"os/signal"
	"syscall"
)

var errAssistedChildBreakawayUnsupported = errors.New("Windows Job breakaway is unavailable")

func platformIgnoreAssistedChildTermination() {
	signal.Ignore(syscall.SIGTERM)
}

func platformConfigureAssistedChildDescendant(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func platformAttemptAssistedChildBreakaway() error {
	return errAssistedChildBreakawayUnsupported
}

func platformAssistedChildProcessAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func platformKillAssistedChildTestProcess(pid int) {
	if pid > 1 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
