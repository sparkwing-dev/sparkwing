//go:build unix

package sparkwing

import (
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

type stepJob struct{}

func startStepCommand(cmd *exec.Cmd, _ string) (stepJob, error) { return stepJob{}, cmd.Start() }

func (stepJob) close() {}

func ownCommandGroup(cmd *exec.Cmd) func() {
	return procgroup.Own(cmd.Process.Pid)
}
