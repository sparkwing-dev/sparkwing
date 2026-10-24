package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// CredentialInventoryReader supplies one tenant's stored secret and git metadata.
type CredentialInventoryReader interface {
	Team() store.Team
	ListSecrets() ([]store.Secret, error)
	GitCredentials(context.Context) ([]store.GitCredential, error)
}

func (s *Server) credentialInventory(ctx context.Context, team string) (CredentialInventoryReader, error) {
	return tenantPort[CredentialInventoryReader](ctx, s, store.Team(team))
}

func (s *Server) credentialEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/secrets", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleListSecrets},
		{Method: "POST", Pattern: "/api/v1/secrets", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleCreateSecret},
		{Method: "GET", Pattern: "/api/v1/secrets/{name}", Authentication: capability.AuthCapability, Decode: secretReadTarget, Handle: s.handleReadSecret},
		{Method: "DELETE", Pattern: "/api/v1/secrets/{name}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteSecret},
		{Method: "POST", Pattern: "/api/v1/secrets/rotate", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRotateSecrets},
		{Method: "GET", Pattern: "/api/v1/team/git-credentials", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleListGitCredentials},
		{Method: "POST", Pattern: "/api/v1/team/git-credentials", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePutGitCredential},
		{Method: "POST", Pattern: "/api/v1/team/git-credentials/{host}/confirm", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleConfirmGitCredential},
		{Method: "DELETE", Pattern: "/api/v1/team/git-credentials/{host}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteGitCredential},
		{Method: "GET", Pattern: "/api/v1/team/git-credentials/releases", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleListGitCredentialReleases},
	}
}

var _ CredentialInventoryReader = (*store.Tenant)(nil)
