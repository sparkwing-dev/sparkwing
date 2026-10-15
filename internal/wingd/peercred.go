package wingd

import (
	"net"
	"os"
)

var readPeerUID = peerUID

func checkPeerCredentials(nc net.Conn) error {
	_, err := acceptedPeerIdentity(nc)
	return err
}

func peerAllowed(uid int, known bool) bool {
	return known && uid == os.Getuid()
}
