//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

func execToolchain(bin string, args, env []string) error {
	// #nosec G702 -- a release binary whose digest the caller matched against the signed release manifest stored beside it
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
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
