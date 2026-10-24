//go:build windows

package gitcmd

import (
	"context"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// Output runs and joins a Git process and its descendants.
func Output(_ context.Context, cmd *exec.Cmd, combined bool) ([]byte, error) {
	return procgroup.CommandOutput(cmd, combined)
}
