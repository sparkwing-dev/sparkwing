//go:build !windows

package git

import (
	"context"
	"os/exec"
)

func runGitCommand(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() }
