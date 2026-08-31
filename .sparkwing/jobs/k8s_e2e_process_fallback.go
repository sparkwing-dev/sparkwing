//go:build !darwin && !linux

package jobs

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func configureKubernetesE2ECommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		err := cmd.Process.Signal(os.Interrupt)
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 90 * time.Second
}
