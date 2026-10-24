package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// RunnerTenant resolves the authenticated claim's request-scoped store view.
func (s *EngineController) RunnerTenant(ctx context.Context, team store.Team) (*store.Tenant, error) {
	return scopedTenantPort[*store.Tenant](ctx, team, s.ResolveTenant)
}
