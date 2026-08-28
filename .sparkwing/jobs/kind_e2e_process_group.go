//go:build darwin || linux

package jobs

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureKindE2ECommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 90 * time.Second
}
