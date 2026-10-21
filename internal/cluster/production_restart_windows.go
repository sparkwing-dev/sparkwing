//go:build windows

package cluster

import (
	"errors"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func restartRunner(executable string, args, environment []string) error {
	command := procenv.Command(
		environment, executable, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			os.Exit(exited.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}
