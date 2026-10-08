//go:build windows

package supervise

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureSupervisorChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// bug: a detached supervisor's child otherwise creates a visible console whose closure stops the daemon.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
