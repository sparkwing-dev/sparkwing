//go:build !windows

package procenv

import "os/exec"

func setCommandLine(*exec.Cmd, string) {}
