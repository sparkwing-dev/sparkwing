//go:build !windows

package orchestrator

import (
	"syscall"
	"testing"
)

func signalSelfInterruptForTest() error {
	return syscall.Kill(syscall.Getpid(), syscall.SIGINT)
}

func runIsolatedInterruptTest(t *testing.T) bool { return false }
