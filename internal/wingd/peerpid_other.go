//go:build !linux && !darwin

package wingd

import "net"

func peerPID(net.Conn) (int, bool) {
	return 0, false
}
