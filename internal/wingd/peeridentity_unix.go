//go:build !windows

package wingd

import (
	"fmt"
	"net"
	"os"
	"runtime"
)

func controllerPeerIdentity() (LocalPeerIdentity, error) {
	uid := uint32(os.Getuid())
	return LocalPeerIdentity{GOOS: runtime.GOOS, UID: &uid}, nil
}

func acceptedPeerIdentity(nc net.Conn) (LocalPeerIdentity, error) {
	uid, known, err := readPeerUID(nc)
	if err != nil {
		return LocalPeerIdentity{}, fmt.Errorf("wingd: read peer credentials: %w", err)
	}
	if !known {
		return LocalPeerIdentity{}, fmt.Errorf("wingd: peer identity unavailable; local API access refused")
	}

	value := uint32(uid)
	peer := LocalPeerIdentity{GOOS: runtime.GOOS, UID: &value}
	owner, err := controllerPeerIdentity()
	if err == nil {
		_, err = LocalSubject(peer, owner)
	}
	if err != nil {
		return LocalPeerIdentity{}, fmt.Errorf("wingd: refused a connection from uid %d; this daemon serves uid %d only: %w", uid, os.Getuid(), err)
	}
	return peer, nil
}
