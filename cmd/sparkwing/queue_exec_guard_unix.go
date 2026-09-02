//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const queueExecGuardDrainInterval = 10 * time.Millisecond

func runQueueExecCommand(command []string) error {
	identity, err := procgroup.CaptureSession(os.Getpid())
	if err != nil {
		return fmt.Errorf("capture command session: %w", err)
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)

	// #nosec G702 -- the command this user asked the queue to run, as argv without a shell
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	commandErr := cmd.Run()
	for {
		quiescent, inspectErr := procgroup.SessionQuiescent(identity)
		if inspectErr != nil {
			return errors.Join(commandErr, fmt.Errorf("inspect command session: %w", inspectErr))
		}
		if quiescent {
			break
		}
		time.Sleep(queueExecGuardDrainInterval)
	}
	if commandErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(commandErr, &exitErr) && exitErr.ExitCode() > 0 {
		return exitError(exitErr.ExitCode(), commandErr)
	}
	return commandErr
}
