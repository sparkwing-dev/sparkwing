//go:build darwin

package wingd

import "golang.org/x/sys/unix"

func peerPIDFromFD(fd uintptr) (int, error) {
	return unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}
