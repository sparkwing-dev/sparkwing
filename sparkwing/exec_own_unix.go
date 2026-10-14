//go:build unix

package sparkwing

import (
	"os/exec"
)

type stepJob struct{}

func (stepJob) wasCancelled() bool { return false }

func startStepCommand(cmd *exec.Cmd, _ string) (stepJob, error) { return stepJob{}, cmd.Start() }

func (stepJob) close() {}
