//go:build !linux && !darwin && !freebsd

package wingd

import "net"

func peerUID(net.Conn) (int, bool, error) {
	return 0, false, nil
}
