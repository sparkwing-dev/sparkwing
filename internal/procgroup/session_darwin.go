//go:build darwin

package procgroup

import (
	"fmt"
	"strconv"
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
		process := *(*unix.KinfoProc)(unsafe.Pointer(&raw[start]))
		if int(process.Proc.P_pid) != pid {
			continue
		}
		sid, err := unix.Getsid(pid)
		if err != nil {
			return 0, "", err
		}
		birth := strconv.FormatInt(process.Proc.P_starttime.Sec, 10) + ":" +
			strconv.FormatInt(int64(process.Proc.P_starttime.Usec), 10)
		return sid, birth, nil
	}
	return 0, "", fmt.Errorf("process %d is absent", pid)
}

func terminateGuardSession(sessionID int) error {
	return signalSession(sessionID, syscall.SIGTERM)
}
