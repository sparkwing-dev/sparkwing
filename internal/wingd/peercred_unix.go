//go:build linux || darwin || freebsd

package wingd

import (
	"net"
	"syscall"
)

func peerUID(nc net.Conn) (int, bool, error) {
	sc, ok := nc.(syscall.Conn)
	if !ok {
		return 0, false, nil
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, false, err
	}
	var uid int
	var inner error
	if err := raw.Control(func(fd uintptr) { uid, inner = peerUIDFromFD(fd) }); err != nil {
		return 0, false, err
	}
	if inner != nil {
		return 0, false, inner
	}
	return uid, true, nil
}
