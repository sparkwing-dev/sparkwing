//go:build !windows

package orchestrator

import "syscall"

func signalSelfInterruptForTest() error {
	return syscall.Kill(syscall.Getpid(), syscall.SIGINT)
}
