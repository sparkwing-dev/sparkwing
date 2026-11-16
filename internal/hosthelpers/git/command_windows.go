//go:build windows

package git

import (
	"context"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func runGitCommand(ctx context.Context, cmd *exec.Cmd) error { return procgroup.RunCommand(ctx, cmd) }
