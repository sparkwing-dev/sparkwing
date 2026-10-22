//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func execToolchain(bin string, args, env []string) error {
	// #nosec G702 -- the engine resolver verified this executable and its release identity
	cmd := procenv.ControlCommand(
		env, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}
