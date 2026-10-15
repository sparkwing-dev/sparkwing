package wingd

import (
	"net"

	"golang.org/x/sys/windows"
)

func peerPID(nc net.Conn) (int, bool) {
	c, ok := nc.(*pipeConn)
	if !ok || !c.server {
		return 0, false
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var pid uint32
	var inner error
	err = raw.Control(func(handle uintptr) { inner = windows.GetNamedPipeClientProcessId(windows.Handle(handle), &pid) })
	return int(pid), err == nil && inner == nil && pid > 0
}
