//go:build windows

package cache

import (
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func configureGitCommand(*exec.Cmd) {}

func gitCommandOutput(cmd *exec.Cmd, combined bool) ([]byte, error) {
	return procgroup.CommandOutput(cmd, combined)
}
