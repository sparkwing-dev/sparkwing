package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// AccountReader reads records belonging to the authenticated account.
type AccountReader interface {
	Account(context.Context, string) (store.Account, error)
	AccountMemberships(context.Context, string) ([]store.Membership, error)
	OpenInvitationsForEmail(context.Context, string) ([]store.Invitation, error)
	AccountIdentities(context.Context, string) ([]store.Identity, error)
	TeamDeletionsRequestedBy(context.Context, string) ([]store.TeamDeletion, error)
}

// WithAccountReader supplies account reads before Handler is constructed.
func (s *Server) WithAccountReader(reader AccountReader) *Server {
	s.accountReads = reader
	return s
}

func (s *Server) accountReader() (AccountReader, error) {
	if s.accountReads != nil {
		return s.accountReads, nil
	}
	return nil, missingEndpointPolicy()
}

func (s *Server) accountEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/me", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleMe},
		{Method: "POST", Pattern: "/api/v1/me/active-team", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleSetActiveTeam},
		{Method: "GET", Pattern: "/api/v1/me/identities", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleIdentities},
		{Method: "DELETE", Pattern: "/api/v1/me/identities/{provider}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleIdentityUnlink},
		{Method: "GET", Pattern: "/api/v1/me/team-deletions", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleMyTeamDeletions},
		{Method: "POST", Pattern: "/api/v1/teams", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleCreateTeam},
		{Method: "POST", Pattern: "/api/v1/invitations/{invitation}/accept", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleAcceptInvitation},
	}
}

var _ AccountReader = (*store.Operator)(nil)
