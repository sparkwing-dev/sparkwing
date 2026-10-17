package controller

import (
	"context"

	"golang.org/x/crypto/ssh"
)

var (
	EngineClaimHTTPFixture = engineClaimHTTPFixture
	EngineClaimHTTP        = engineClaimHTTP
)

// CardBillingPass runs one pass of the payment worker, so a test drives its
// schedule instead of waiting on the ticker.
func (s *Server) CardBillingPass(ctx context.Context) { s.cardBillingPass(ctx) }

// SetHostKeyScan replaces the ssh host key read a new git credential makes,
// so a test serves a key without dialing a host.
func SetHostKeyScan(s *Server, scan func(ctx context.Context, host string, port int) (ssh.PublicKey, error)) {
	s.hostKeyScan = scan
}
