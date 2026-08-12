//go:build !windows

package supervise

import "syscall"

// signalTerminate asks pid to shut down gracefully (SIGTERM on POSIX).
// The supervisor escalates to signalKill if the child does not exit
// within its grace window.
func signalTerminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// signalKill force-stops pid (SIGKILL on POSIX). The daemon gets no
// chance to clean up; its state file is the recovery record.
func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
