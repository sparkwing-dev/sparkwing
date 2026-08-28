//go:build !windows

package main

import (
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

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

func signalTerminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
