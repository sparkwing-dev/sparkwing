package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) tenantForTeam(ctx context.Context, team store.Team) (*store.Tenant, error) {
	return tenantPort[*store.Tenant](ctx, s, team)
}

func (s *EngineController) tenantForTeam(ctx context.Context, team store.Team) (*store.Tenant, error) {
	handle, err := scopedTenant(ctx, team, func(ctx context.Context, team store.Team) (TenantHandle, error) {
		if s.Store == nil {
			return nil, missingEndpointPolicy()
		}
		return s.Store.ForTeam(ctx, team)
	})
	if err != nil {
		return nil, err
	}
	tenant, ok := handle.(*store.Tenant)
	if !ok {
		return nil, missingEndpointPolicy()
	}
	return tenant, nil
}
