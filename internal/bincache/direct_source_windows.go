package bincache

import (
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func killGroupOnCancel(*exec.Cmd) {}

func gitCommandCombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	return procgroup.CommandOutput(cmd, true)
}

func gitCommandOutput(cmd *exec.Cmd) ([]byte, error) {
	return procgroup.CommandOutput(cmd, false)
}
