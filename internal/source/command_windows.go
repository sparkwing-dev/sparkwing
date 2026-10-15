//go:build windows

package source

import (
	"context"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func commandOutput(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	return procgroup.CommandOutput(cmd, false)
}
