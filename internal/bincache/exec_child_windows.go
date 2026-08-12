//go:build windows

package bincache

import (
	"errors"
	"os"
	"os/exec"
)

func execChild(bin string, args []string, env []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode()) //nolint:forbidigo // foreground wrapper preserves the pipeline's exit status
		}
		return err
	}
	os.Exit(0) //nolint:forbidigo // foreground wrapper preserves the pipeline's exit status
	return nil
}
