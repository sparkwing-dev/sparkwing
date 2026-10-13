//go:build unix

package repos

import (
	"os"
	"os/exec"
	"syscall"
)

type processTree struct{ group int }

// safety: the child leads its own process group, so a kill reaches what it
// started; the group is signaled only before Wait reaps the leader.
func startTree(cmd *exec.Cmd, requests, frames *os.File) (*processTree, error) {
	cmd.ExtraFiles = []*os.File{requests, frames}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processTree{group: cmd.Process.Pid}, nil
}

func (t *processTree) kill() {
	_ = syscall.Kill(-t.group, syscall.SIGKILL) //nolint:errcheck // a group that already exited has nothing left to kill
}

func (t *processTree) release() {}
