//go:build !windows

package supervise

import "syscall"

func signalTerminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
