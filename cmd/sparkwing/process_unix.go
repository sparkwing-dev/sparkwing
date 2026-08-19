//go:build !windows

package main

import (
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// processAlive reports whether pid refers to a running process. A zombie still
// answers kill(pid, 0), but has already finished and must not hold shutdown or
// replacement waits open.
func processAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	processes, err := procgroup.List()
	if err != nil {
		return true
	}
	for _, process := range processes {
		if process.PID != pid {
			continue
		}
		if process.State == "" {
			return true
		}
		switch process.State[0] {
		case 'Z', 'X', 'x':
			return false
		default:
			return true
		}
	}
	return false
}

// signalTerminate asks pid to shut down gracefully (SIGTERM on POSIX).
// Callers should poll processAlive and escalate to signalKill if the
// process doesn't exit within a deadline.
func signalTerminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// signalKill force-stops pid (SIGKILL on POSIX). The process gets no
// chance to clean up.
func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
