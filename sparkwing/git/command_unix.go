//go:build !windows

package git

import "os/exec"

func runGitCommand(cmd *exec.Cmd) error { return cmd.Run() }
