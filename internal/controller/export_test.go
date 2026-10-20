package controller

import (
	"context"

	"golang.org/x/crypto/ssh"
)

var (
	EngineAcceptedFixturePlan = engineAcceptedFixturePlan
	EngineClaimHTTPFixture    = engineClaimHTTPFixture
	EngineClaimHTTP           = engineClaimHTTP
	ChildIntakeFixture        = childIntakeFixture
	EngineSQL                 = engineSQL
	EngineHTTPRoot            = engineHTTPRoot
	AssertedProvenance        = assertedProvenance
	SealWith                  = sealWith
)

// SetHostKeyScan replaces the ssh host key read a new git credential makes,
// so a test serves a key without dialing a host.
func SetHostKeyScan(s *Server, scan func(ctx context.Context, host string, port int) (ssh.PublicKey, error)) {
	s.hostKeyScan = scan
}
