//go:build darwin

package procgroup

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func waitLeaderExit(pid int) error {
	for {
		var info [128]byte
		_, _, errno := syscall.Syscall6(
			syscall.SYS_WAITID,
			1,
			uintptr(pid),
			uintptr(unsafe.Pointer(&info[0])),
			uintptr(unix.WEXITED|unix.WNOWAIT),
			0,
			0,
		)
		if errors.Is(errno, syscall.EINTR) {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}
