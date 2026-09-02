package wingd

import (
	"fmt"
	"net"
	"os"
)

var readPeerUID = peerUID

func checkPeerCredentials(nc net.Conn) error {
	uid, known, err := readPeerUID(nc)
	if err != nil {
		return fmt.Errorf("wingd: read peer credentials: %w", err)
	}
	if peerAllowed(uid, known) {
		return nil
	}
	return fmt.Errorf("wingd: refused a connection from uid %d; this daemon serves uid %d only", uid, os.Getuid())
}

func peerAllowed(uid int, known bool) bool {
	return !known || uid == os.Getuid()
}
