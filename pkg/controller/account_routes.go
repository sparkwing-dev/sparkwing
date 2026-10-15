package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// AccountReader reads records belonging to the authenticated account.
type AccountReader interface {
	Account(context.Context, string) (store.Account, error)
	AccountMemberships(context.Context, string) ([]store.Membership, error)
	OpenInvitationsForEmail(context.Context, string, time.Time) ([]store.Invitation, error)
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
	if s.store == nil {
		return nil, missingEndpointPolicy()
	}
	return s.store, nil
}

func (s *Server) accountEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/me", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleMe},
		{Method: "GET", Pattern: "/api/v1/me/identities", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleIdentities},
		{Method: "GET", Pattern: "/api/v1/me/team-deletions", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleMyTeamDeletions},
	}
}

func (s *Server) registerAccountRoutes(mux *http.ServeMux) {
	router := NewPolicyRouter(s.authorization)
	for _, endpoint := range s.accountEndpoints() {
		if err := router.Register(endpoint); err != nil {
			mux.HandleFunc(endpoint.Method+" "+endpoint.Pattern, func(w http.ResponseWriter, _ *http.Request) { writePolicyError(w, err) })
			continue
		}
		pattern := endpoint.Pattern
		if pattern == "/api/v1/me/identities" {
			pattern = endpoint.Method + " " + pattern
		}
		mux.Handle(pattern, noStore(router))
	}
}

var _ AccountReader = (*store.Store)(nil)
