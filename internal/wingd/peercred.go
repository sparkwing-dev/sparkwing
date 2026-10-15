package wingd

import (
	"fmt"
	"net"
	"os"
	"runtime"
)

var readPeerUID = peerUID

func checkPeerCredentials(nc net.Conn) error {
	_, err := acceptedPeerIdentity(nc)
	return err
}

func acceptedPeerIdentity(nc net.Conn) (LocalPeerIdentity, error) {
	uid, known, err := readPeerUID(nc)
	if err != nil {
		return LocalPeerIdentity{}, fmt.Errorf("wingd: read peer credentials: %w", err)
	}
	if !known {
		return LocalPeerIdentity{}, fmt.Errorf("wingd: peer identity unavailable; local API access refused")
	}
	if !peerAllowed(uid, known) {
		return LocalPeerIdentity{}, fmt.Errorf("wingd: refused a connection from uid %d; this daemon serves uid %d only", uid, os.Getuid())
	}
	value := uint32(uid)
	return LocalPeerIdentity{GOOS: runtime.GOOS, UID: &value}, nil
}

func peerAllowed(uid int, known bool) bool {
	return known && uid == os.Getuid()
}
