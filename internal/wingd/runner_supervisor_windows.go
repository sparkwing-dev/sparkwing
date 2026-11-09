//go:build windows

package wingd

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureDetachedLocalRunner(command *exec.Cmd) error {
	// safety: console detachment alone leaves the runner inside a controller-owned Job Object.
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_BREAKAWAY_FROM_JOB,
	}
	return nil
}
