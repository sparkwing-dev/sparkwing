//go:build darwin

package procgroup

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func guardedSessionSupport() error { return nil }

func sessionIdentity(pid int) (int, string, error) {
	raw, err := unix.SysctlRaw("kern.proc.all")
	if err != nil {
		return 0, "", err
	}
	size := int(unsafe.Sizeof(unix.KinfoProc{}))
	for start := 0; size > 0 && start+size <= len(raw); start += size {
		// #nosec G103 -- decodes a fixed-size kernel struct from a bounds-checked buffer
		process := *(*unix.KinfoProc)(unsafe.Pointer(&raw[start]))
		if int(process.Proc.P_pid) != pid {
			continue
		}
		sid, err := unix.Getsid(pid)
		if err != nil {
			return 0, "", err
		}
		return sid, darwinBirthToken(process), nil
	}
	return 0, "", fmt.Errorf("%w: process %d", ErrProcessAbsent, pid)
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
