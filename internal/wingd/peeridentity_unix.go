//go:build !windows

package wingd

import (
	"fmt"
	"net"
	"os"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func controllerPeerIdentity() (localhostwire.LocalPeerIdentity, error) {
	uid := uint32(os.Getuid())
	return localhostwire.LocalPeerIdentity{GOOS: runtime.GOOS, UID: &uid}, nil
}

func acceptedPeerIdentity(nc net.Conn) (localhostwire.LocalPeerIdentity, error) {
	uid, known, err := readPeerUID(nc)
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, fmt.Errorf("wingd: read peer credentials: %w", err)
	}
	if !known {
		return localhostwire.LocalPeerIdentity{}, fmt.Errorf("wingd: peer identity unavailable; local API access refused")
	}

	value := uint32(uid)
	peer := localhostwire.LocalPeerIdentity{GOOS: runtime.GOOS, UID: &value}
	owner, err := controllerPeerIdentity()
	if err == nil {
		_, err = LocalSubject(peer, owner)
	}
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, fmt.Errorf("wingd: refused a connection from uid %d; this daemon serves uid %d only: %w", uid, os.Getuid(), err)
	}
	return peer, nil
}
