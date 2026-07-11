//go:build !windows

package sparkwing

import (
	"os/exec"
	"syscall"
)

func prepareCommandForRun(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func requestCommandStop(cmd *exec.Cmd) {
	signalCommand(cmd, syscall.SIGTERM)
}

func forceCommandStop(cmd *exec.Cmd) {
	signalCommand(cmd, syscall.SIGKILL)
}

func signalCommand(cmd *exec.Cmd, signal syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, signal)
}
