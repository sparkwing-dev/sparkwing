//go:build !windows

package bincache

import (
	"os/exec"
	"syscall"
)

// safety: Cancellation must also stop git transport children that would keep connections and pipes open.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
