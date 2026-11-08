//go:build windows

package git

import (
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func runGitCommand(cmd *exec.Cmd) error { return procgroup.RunCommand(cmd) }
