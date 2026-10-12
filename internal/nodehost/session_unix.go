//go:build unix

package nodehost

import (
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func startProcess(cmd *exec.Cmd, requests, frames *os.File) (sessionProcess, error) {
	cmd.ExtraFiles = []*os.File{requests, frames}
	return procgroup.StartSession(cmd)
}
