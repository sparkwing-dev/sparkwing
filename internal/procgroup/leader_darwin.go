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
		// safety: waitid writes a siginfo_t here; 128 bytes exceeds its size on every darwin ABI.
		var info [128]byte
		_, _, errno := syscall.Syscall6(
			syscall.SYS_WAITID,
			1,
			uintptr(pid),
			// #nosec G103 -- hands the kernel a stack buffer larger than the siginfo_t it writes
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
