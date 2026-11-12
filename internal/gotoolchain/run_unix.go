//go:build !windows

package gotoolchain

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const toolchainTerminationTimeout = 2*procgroup.DefaultTerminationGrace + 30*time.Second

func runToolchain(ctx context.Context, cmd *exec.Cmd) error {
	group, err := procgroup.Start(cmd)
	if err != nil {
		return err
	}
	group.SetDescendantProbe(toolchainDescendantProbe)
	waited := make(chan error, 1)
	go func() { waited <- group.Finish(context.WithoutCancel(ctx), procgroup.DefaultTerminationGrace) }()

	select {
	case waitErr := <-waited:
		//nolint:contextcheck // Group.Kill owns its own bounded process-table read and has no caller-context variant.
		return toolchainResult(group, cmd, waitErr)
	case <-ctx.Done():
	}

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolchainTerminationTimeout)
	defer cancel()
	attribution, terminateErr := group.TerminateForCancellation(termCtx, procgroup.DefaultTerminationGrace)
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-termCtx.Done():
		return errors.Join(ctx.Err(), terminateErr, context.Cause(termCtx))
	}
	if attribution.Caused(waitErr) {
		waitErr = ctx.Err()
	}
	if attribution.Caused(terminateErr) {
		terminateErr = ctx.Err()
	}
	return errors.Join(ctx.Err(), terminateErr, waitErr)
}

// hack: a test replaces the platform descendant probe here to drive the cleanup
// paths without a live process tree.
var toolchainDescendantProbe func(group int, exited, session bool) (bool, error)

// safety: the compiler has already exited by the time cleanup runs, so a
// cleanup failure must not stand in for the build's own status. The group is
// killed first because an unverified descendant still holding the output pipe
// would otherwise park the wait.
func toolchainResult(group *procgroup.Group, cmd *exec.Cmd, err error) error {
	if err == nil || !errors.Is(err, procgroup.ErrCleanup) {
		return err
	}
	if killErr := group.Kill(); killErr != nil {
		slog.Default().Debug("toolchain group kill after cleanup failure", "group", group.ID(), "err", killErr)
	}
	result := cmd.Wait()
	slog.Default().Warn("toolchain process group cleanup failed; judging the build on its own exit",
		"group", group.ID(), "err", err)
	return result
}
