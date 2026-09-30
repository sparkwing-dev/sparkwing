//go:build linux || darwin

package wingd

import (
	"net"
	"syscall"
)

func peerPID(nc net.Conn) (int, bool) {
	sc, ok := nc.(syscall.Conn)
	if !ok {
		return 0, false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var pid int
	var inner error
	if err := raw.Control(func(fd uintptr) { pid, inner = peerPIDFromFD(fd) }); err != nil || inner != nil {
		return 0, false
	}
	return pid, true
}
