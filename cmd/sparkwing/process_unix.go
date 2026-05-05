//go:build !windows

package main

import "syscall"

// processAlive reports whether pid refers to a running process. On POSIX,
// kill(pid, 0) is the canonical liveness probe -- it sends no signal but
// returns ESRCH for a dead pid.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
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
