package controller

import (
	"context"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TeamBoundaryExempt exposes the routes the team boundary leaves to their own
// gate, so the external boundary test can hold every other run route to it.
var TeamBoundaryExempt = teamBoundaryExempt

// MuxRouteScopes returns every route server.go registers on the authenticated
// mux with the scope requireScope gates it on, read from the source so a route
// added later is in the list without anyone adding it.
func MuxRouteScopes(t *testing.T) map[string]string {
	t.Helper()
	return muxRoutes(t, "server.go")
}

// SetHostKeyScan replaces the ssh host key read a new git credential makes,
// so a test serves a key without dialing a host.
func SetHostKeyScan(s *Server, scan func(ctx context.Context, host string, port int) (ssh.PublicKey, error)) {
	s.hostKeyScan = scan
}
