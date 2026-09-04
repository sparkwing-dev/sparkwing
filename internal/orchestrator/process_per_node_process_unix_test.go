//go:build !windows

package orchestrator_test

import "syscall"

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func killProcessForTest(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
