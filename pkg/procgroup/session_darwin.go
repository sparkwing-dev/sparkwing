//go:build darwin

package procgroup

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func guardedSessionSupport() error { return nil }

var darwinSessionProcess = unix.SysctlKinfoProcSlice

func sessionIdentity(pid int) (int, string, error) {
	processes, err := darwinSessionProcess("kern.proc.pid", pid)
	if err != nil {
		return 0, "", err
	}
	if len(processes) != 1 || int(processes[0].Proc.P_pid) != pid {
		return 0, "", fmt.Errorf("%w: process %d", ErrProcessAbsent, pid)
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return 0, "", err
	}
	return sid, darwinBirthToken(processes[0]), nil
}

func signalGuardSession(sessionID int, kill bool) error {
	signal := syscall.SIGTERM
	if kill {
		signal = syscall.SIGKILL
	}
	return signalSession(sessionID, signal)
}

func signalDiagnosticSession(sessionID int) error {
	return signalSession(sessionID, syscall.SIGQUIT)
}
