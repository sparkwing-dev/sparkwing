//go:build windows

package cluster

import (
	"errors"
	"os"
	"os/exec"
)

func restartRunner(executable string, args, environment []string) error {
	command := exec.Command(executable, args...)
	command.Stdin, command.Stdout, command.Stderr, command.Env = os.Stdin, os.Stdout, os.Stderr, environment
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
