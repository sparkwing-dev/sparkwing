package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// TeamWriter commits team administration under current authority and membership.
type TeamWriter interface {
	Team() store.Team
	RenameAuthorizedTeam(context.Context, capability.Authorized, string) (store.TeamInfo, error)
	DeleteAuthorizedTeam(context.Context, capability.Authorized) (store.TeamDeletion, error)
	CreateAuthorizedInvitation(context.Context, capability.Authorized, string, store.Role, string) (store.Invitation, error)
	DeleteAuthorizedInvitation(context.Context, capability.Authorized, string) error
	SetAuthorizedBuildTrust(context.Context, capability.Authorized, bool) error
	DeleteAuthorizedRun(context.Context, capability.Authorized) error
}

// WithTeamWriter supplies tenant administration transactions before Handler is constructed.
func (s *Server) WithTeamWriter(resolve func(context.Context, store.Team) (TeamWriter, error)) *Server {
	s.resolveTeamWriter = resolve
	return s
}

func (s *Server) teamWriter(r *http.Request, a capability.Authorized) (TeamWriter, error) {
	if a.Target.Team == "" {
		return nil, missingEndpointPolicy()
	}
	var writer TeamWriter
	var err error
	if s.resolveTeamWriter != nil {
		writer, err = s.resolveTeamWriter(r.Context(), store.Team(a.Target.Team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.reportingTenant(r, a)
		writer, _ = any(tenant).(TeamWriter)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if writer == nil || writer.Team() != store.Team(a.Target.Team) {
		return nil, missingEndpointPolicy()
	}
	return writer, nil
}

func (s *Server) teamWriteEndpoints() []Endpoint {
	engine := s.engineController
	return []Endpoint{
		{Method: "PUT", Pattern: "/api/v1/team/settings", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleTeamSettings},
		{Method: "PATCH", Pattern: "/api/v1/team", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRenameTeam},
		{Method: "DELETE", Pattern: "/api/v1/team", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteTeam},
		{Method: "POST", Pattern: "/api/v1/team/invitations", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleInvite},
		{Method: "DELETE", Pattern: "/api/v1/team/invitations/{invitation}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteInvitation},
		{Method: "PUT", Pattern: "/api/v1/team/build-trust", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePutBuildTrust},
		{Method: "DELETE", Pattern: "/api/v1/runs/{run}", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleDeleteRun},
	}
}

var _ TeamWriter = (*store.Tenant)(nil)
