//go:build !windows

package releaseasset

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func runProbeProcess(ctx context.Context, cmd *exec.Cmd, cleanupTimeout time.Duration) error {
	group, err := procgroup.StartSession(cmd)
	if err != nil {
		return err
	}
	err = group.Finish(ctx, cleanupTimeout/2)
	if err == nil || group.Reaped() {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if cleanupErr := group.Terminate(cleanupCtx, cleanupTimeout/2); cleanupErr != nil {
		return errors.Join(err, cleanupErr)
	}
	return err
}
