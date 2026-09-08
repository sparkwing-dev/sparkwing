//go:build !unix && !windows

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

type stepJob struct{}

func startStepCommand(cmd *exec.Cmd, _ string) (stepJob, error) { return stepJob{}, cmd.Start() }

func (stepJob) close() {}

func ownCommandGroup(*exec.Cmd) func() { return func() {} }

func recordStepSession(context.Context, *exec.Cmd, string, stepJob) func() { return func() {} }

func commandResourceUsage(cmd *exec.Cmd) (time.Duration, int64, bool) {
	return 0, 0, false
}
