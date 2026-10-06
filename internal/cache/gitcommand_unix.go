//go:build !windows

package cache

import (
	"os/exec"
	"syscall"
)

func configureGitCommand(cmd *exec.Cmd) {
	// safety: process group kill prevents SSH child orphans on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func gitCommandOutput(cmd *exec.Cmd, combined bool) ([]byte, error) {
	if combined {
		return cmd.CombinedOutput()
	}
	return cmd.Output()
}
