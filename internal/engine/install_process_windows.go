package engine

import (
	"context"
	"errors"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/pkg/procgroup"
)

func runSDKInstallProcess(ctx context.Context, command *exec.Cmd) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	job, err := procgroup.NewJob("")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, job.Close()) }()
	command.WaitDelay = procgroup.DefaultTerminationGrace
	if err := job.Start(command); err != nil {
		if command.Process != nil {
			err = errors.Join(err, command.Wait())
		}
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.Join(ctx.Err(), job.Terminate(), <-done)
	}
	return errors.Join(err, job.Terminate())
}
