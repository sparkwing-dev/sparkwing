//go:build !windows

package nodehost

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// safety: bounds the kill of what a cleanup left behind once it exits or times out.
const cleanupSettleTimeout = 30 * time.Second

// safety: the cleanup leads its own session, so every process it starts,
// backgrounded or not, ends with it instead of outliving its time limit.
func runCleanupProcess(ctx context.Context, cmd *exec.Cmd) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	group, err := procgroup.StartSession(cmd)
	if err != nil {
		return err
	}
	stopKill := context.AfterFunc(ctx, func() { _ = group.Kill() })
	defer stopKill()
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupSettleTimeout)
	defer cancel()
	select {
	case <-group.LeaderExited():
		err = group.Finish(settle, procgroup.DefaultTerminationGrace)
	case <-ctx.Done():
		err = errors.Join(group.Kill(), group.Finish(settle, 0))
	}
	if errors.Is(err, procgroup.ErrCleanup) {
		// safety: a failed settle leaves the leader unreaped and descendants possibly live.
		//nolint:contextcheck // Group.Kill bounds its own process-table read and has no context variant.
		err = errors.Join(err, group.Kill(), cmd.Wait())
	}
	return err
}
