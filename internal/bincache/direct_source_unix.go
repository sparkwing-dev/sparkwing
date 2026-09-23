//go:build !windows

package bincache

import (
	"os/exec"
	"syscall"
)

// killGroupOnCancel makes a cancelled git take its transport helpers with it:
// an orphaned ssh or git-remote-https would otherwise keep the connection, and
// the output pipe, open past the timeout.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
