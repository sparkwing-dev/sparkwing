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

// safety: a platform that reports no peer uid has already passed
// peerAllowed, and this daemon serves its own account only, so the caller is
// named after the account the daemon runs as rather than left unattributed.
func acceptedPeerUID(nc net.Conn) (int, error) {
	uid, known, err := readPeerUID(nc)
	if err != nil {
		return 0, fmt.Errorf("wingd: read peer credentials: %w", err)
	}
	if !peerAllowed(uid, known) {
		return 0, fmt.Errorf("wingd: refused a connection from uid %d; this daemon serves uid %d only", uid, os.Getuid())
	}
	if !known {
		return os.Getuid(), nil
	}
	return uid, nil
}

func peerAllowed(uid int, known bool) bool {
	return !known || uid == os.Getuid()
}
