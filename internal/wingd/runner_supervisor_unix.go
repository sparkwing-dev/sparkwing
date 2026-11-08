//go:build linux || darwin

package wingd

import (
	"os/exec"
	"syscall"
)

func configureDetachedLocalRunner(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
