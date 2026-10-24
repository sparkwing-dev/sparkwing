package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// teamWriter commits team administration under current authority and membership.
type teamWriter interface {
	Team() store.Team
	RenameAuthorizedTeam(context.Context, capability.Authorized, string) (store.TeamInfo, error)
	DeleteAuthorizedTeam(context.Context, capability.Authorized) (store.TeamDeletion, error)
	CreateAuthorizedInvitation(context.Context, capability.Authorized, string, store.Role, string) (store.Invitation, error)
	DeleteAuthorizedInvitation(context.Context, capability.Authorized, string) error
	SetAuthorizedBuildTrust(context.Context, capability.Authorized, bool) error
	DeleteAuthorizedRun(context.Context, capability.Authorized) error
}

func (s *Server) teamWriter(r *http.Request, a capability.Authorized) (teamWriter, error) {
	return tenantPort[teamWriter](r.Context(), s, store.Team(a.Target.Team))
}

func (s *Server) teamWriteEndpoints() []controllerwire.Endpoint {
	engine := s.engineController
	return []controllerwire.Endpoint{
		{Method: "PUT", Pattern: "/api/v1/team/settings", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleTeamSettings},
		{Method: "PATCH", Pattern: "/api/v1/team", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRenameTeam},
		{Method: "DELETE", Pattern: "/api/v1/team", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteTeam},
		{Method: "POST", Pattern: "/api/v1/team/invitations", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleInvite},
		{Method: "DELETE", Pattern: "/api/v1/team/invitations/{invitation}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteInvitation},
		{Method: "PUT", Pattern: "/api/v1/team/build-trust", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePutBuildTrust},
		{Method: "DELETE", Pattern: "/api/v1/runs/{run}", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleDeleteRun},
	}
}

var _ teamWriter = (*store.Tenant)(nil)
