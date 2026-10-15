package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CredentialInventoryReader supplies one tenant's stored secret and git metadata.
type CredentialInventoryReader interface {
	Team() store.Team
	ListSecrets() ([]store.Secret, error)
	GitCredentials(context.Context) ([]store.GitCredential, error)
}

// WithCredentialInventoryReader supplies tenant inventory reads before Handler is constructed.
func (s *Server) WithCredentialInventoryReader(resolve func(context.Context, store.Team) (CredentialInventoryReader, error)) *Server {
	s.resolveCredentialInventory = resolve
	return s
}

func (s *Server) credentialInventory(ctx context.Context, team string) (CredentialInventoryReader, error) {
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	var reader CredentialInventoryReader
	var err error
	if s.resolveCredentialInventory != nil {
		reader, err = s.resolveCredentialInventory(ctx, store.Team(team))
	} else if s.store != nil {
		reader, err = s.store.ForTeam(ctx, store.Team(team))
	} else {
		return nil, missingEndpointPolicy()
	}
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return nil, engineLookupError(store.ErrNotFound)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if reader == nil || reader.Team() != store.Team(team) {
		return nil, missingEndpointPolicy()
	}
	return reader, nil
}

func (s *Server) registerCredentialInventoryRoutes(mux *http.ServeMux) {
	endpoints := []Endpoint{
		{Method: "GET", Pattern: "/api/v1/secrets", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleListSecrets},
		{Method: "GET", Pattern: "/api/v1/team/git-credentials", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleListGitCredentials},
	}
	for _, endpoint := range endpoints {
		router := NewPolicyRouter(s.authorization)
		var handler http.Handler = router
		if err := router.Register(endpoint); err != nil {
			handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writePolicyError(w, err) })
		}
		mux.Handle(endpoint.Method+" "+endpoint.Pattern, noStore(handler))
	}
}

var _ CredentialInventoryReader = (*store.Tenant)(nil)
