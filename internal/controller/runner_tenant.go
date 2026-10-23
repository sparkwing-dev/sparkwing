package controller

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ValidateEmbeddedRunner checks dependencies before the embedded queue starts.
func (s *EngineController) ValidateEmbeddedRunner() error {
	if s == nil || s.ResolveTenant == nil || s.RunnerPolicy == nil {
		return errors.New("embedded runner requires the owned controller, claimant, signed policy and native services")
	}
	return nil
}

// RunnerTenant resolves the authenticated claim's request-scoped store view.
func (s *EngineController) RunnerTenant(ctx context.Context, team store.Team) (*store.Tenant, error) {
	return scopedTenantPort[*store.Tenant](ctx, team, s.ResolveTenant)
}
