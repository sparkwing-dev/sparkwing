//go:build windows

package bincache

import (
	"errors"
	"os"
	"os/exec"
)

func execChild(bin string, args, env []string, afterChild func()) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
	err := cmd.Run()
	if afterChild != nil {
		afterChild()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode()) //nolint:forbidigo // foreground wrapper preserves the pipeline's exit status
		}
		return err
	}
	os.Exit(0) //nolint:forbidigo // foreground wrapper preserves the pipeline's exit status
	return nil
}
