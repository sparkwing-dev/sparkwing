//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package secrets

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const valuesChannelSupported = true

func inheritedValuesFile(fd int) *os.File {
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "sparkwing-mask-values")
}

func (v *ChildValues) pump() {
	defer v.finish()
	rc, err := v.r.SyscallConn()
	if err != nil {
		return
	}
	buf := make([]byte, 32<<10)
	_ = rc.Read(func(fd uintptr) bool { //nolint:errcheck // a closed pipe is how the pump ends
		for {
			// safety: bytes leave the pipe and their values are registered
			// under one hold of mu, so Mask never sees an empty pipe while a
			// value it read is still unregistered.
			v.mu.Lock()
			n, err := syscall.Read(int(fd), buf)
			if n > 0 {
				v.take(buf[:n])
			}
			v.cond.Broadcast()
			v.mu.Unlock()
			switch {
			case n > 0, errors.Is(err, syscall.EINTR):
			case errors.Is(err, syscall.EAGAIN):
				return false
			default:
				return true
			}
		}
	})
}

// safety: the caller holds mu, so no read can drain the pipe between this count and its wait.
func (v *ChildValues) pending() int {
	rc, err := v.r.SyscallConn()
	if err != nil {
		return 0
	}
	n := 0
	if err := rc.Control(func(fd uintptr) {
		if got, err := unix.IoctlGetInt(int(fd), fionread); err == nil {
			n = got
		}
	}); err != nil {
		return 0
	}
	return n
}
