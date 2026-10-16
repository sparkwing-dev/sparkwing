//go:build !windows

package orchestrator

import (
	"errors"
	"os"
	"syscall"
)

func openRefWorktreeLease(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func inheritableLease(lease []*os.File) []*os.File {
	return lease
}

// KeepInheritedLease keeps the worktree lease a run's host receives on
// descriptor 3 open for the host's life and out of every process it starts.
func KeepInheritedLease() error {
	var st syscall.Stat_t
	if err := syscall.Fstat(3, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return errors.New("descriptor 3 holds no worktree lease; only a sparkwing consumer passes --" + TriggerHostLeaseFlag)
	}
	syscall.CloseOnExec(3)
	return nil
}
