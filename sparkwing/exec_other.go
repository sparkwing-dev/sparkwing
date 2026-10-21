//go:build !unix && !windows

package sparkwing

import (
	"context"
	"os/exec"
	"time"
)

func commandContext(ctx context.Context, environment []string, name string, args ...string) *exec.Cmd {
	return processCommandContext(ctx, environment, name, args...)
}

func configureProcessGroup(context.Context, *exec.Cmd, <-chan struct{}) {}

type stepJob struct{}

func (stepJob) wasCancelled() bool { return false }

func startStepCommand(cmd *exec.Cmd, _ string) (stepJob, error) { return stepJob{}, cmd.Start() }

func (stepJob) close() {}

func recordStepSession(context.Context, *exec.Cmd, string, stepJob) func() { return func() {} }

func commandResourceUsage(cmd *exec.Cmd) (time.Duration, int64, bool) {
	return 0, 0, false
}
