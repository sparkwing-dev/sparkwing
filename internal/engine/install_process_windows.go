package engine

import (
	"context"
	"errors"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
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
	return errors.Join(job.Run(ctx, command), job.Terminate())
}
