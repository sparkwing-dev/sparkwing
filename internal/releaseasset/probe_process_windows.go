//go:build windows

package releaseasset

import (
	"context"
	"os/exec"
	"time"
)

func runProbeProcess(_ context.Context, cmd *exec.Cmd, _ time.Duration) error {
	// Windows has no exact process-tree ownership primitive in this repository;
	// WaitDelay still bounds inherited pipe handles after CommandContext kills the leader.
	return cmd.Run()
}
