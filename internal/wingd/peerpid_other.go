//go:build !linux && !darwin && !windows

package wingd

import "net"

func peerPID(net.Conn) (int, bool) {
	return 0, false
}
