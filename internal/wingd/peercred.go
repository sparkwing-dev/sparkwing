package wingd

import (
	"net"
)

var readPeerUID = peerUID

func checkPeerCredentials(nc net.Conn) error {
	_, err := acceptedPeerIdentity(nc)
	return err
}
