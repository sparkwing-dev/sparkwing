package wingd

import (
	"fmt"
	"net"
	"os"
)

var readPeerUID = peerUID

func checkPeerCredentials(nc net.Conn) error {
	_, err := acceptedPeerUID(nc)
	return err
}

func acceptedPeerUID(nc net.Conn) (int, error) {
	uid, known, err := readPeerUID(nc)
	if err != nil {
		return 0, fmt.Errorf("wingd: read peer credentials: %w", err)
	}
	if !known {
		return 0, fmt.Errorf("wingd: peer identity unavailable; local API access refused")
	}
	if !peerAllowed(uid, known) {
		return 0, fmt.Errorf("wingd: refused a connection from uid %d; this daemon serves uid %d only", uid, os.Getuid())
	}
	return uid, nil
}

func peerAllowed(uid int, known bool) bool {
	return known && uid == os.Getuid()
}
