//go:build !windows

package supervise

import "os/exec"

func configureSupervisorChild(*exec.Cmd) {}
