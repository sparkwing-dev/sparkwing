//go:build windows

package sparkwing

import "os/exec"

func prepareCommandForRun(cmd *exec.Cmd) {
}

func requestCommandStop(cmd *exec.Cmd) {
	forceCommandStop(cmd)
}

func forceCommandStop(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
