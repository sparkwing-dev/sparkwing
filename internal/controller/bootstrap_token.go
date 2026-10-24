package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
)

// BootstrapAdminPrincipal identifies the first deployment administrator.
const BootstrapAdminPrincipal = "bootstrap:admin"

// BootstrapAdminIssuer commits a seeded root through Issue's once-only bootstrap gate.
type BootstrapAdminIssuer interface {
	EnsureBootstrapCapability(context.Context, capability.ProvisionSpec) (bool, error)
}

// EnsureBootstrapAdminToken sends the mounted presentation to the gated capability issuer.
func EnsureBootstrapAdminToken(ctx context.Context, issuer BootstrapAdminIssuer, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	spec := capability.ProvisionSpec{
		Kind: capability.ProvisionBootstrap, Seed: raw,
		Subject: capability.Subject{Kind: capability.SubjectOperator, ID: BootstrapAdminPrincipal},
		Binding: capability.Binding{Kind: capability.BindingNone},
		Grants:  []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore},
	}
	if err := capability.ValidateProvisionSeed(spec); err != nil {
		return false, err
	}
	if issuer == nil {
		return false, routepolicy.MissingEndpointPolicy()
	}
	return issuer.EnsureBootstrapCapability(ctx, spec)
}
