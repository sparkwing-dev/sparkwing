//go:build !unix

package sparkwing

import (
	"context"
	"os/exec"
	"time"
)

func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func configureProcessGroup(context.Context, *exec.Cmd, <-chan struct{}) {}

func commandResourceUsage(cmd *exec.Cmd) (time.Duration, int64, bool) {
	return 0, 0, false
}
