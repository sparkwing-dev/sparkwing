//go:build unix

package sparkwing

import (
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func ownCommandGroup(cmd *exec.Cmd) func() {
	return procgroup.Own(cmd.Process.Pid)
}
