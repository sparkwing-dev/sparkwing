package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ProvisionPolicy verifies external origins and may further restrict store-verified enrollment.
type ProvisionPolicy interface {
	AuthorizeProvision(context.Context, capability.AuthorityResolver, capability.ProvisionSpec) error
}

// SetProvisionPolicy installs trusted origin verification before concurrent store use.
func (s *Store) SetProvisionPolicy(policy ProvisionPolicy) { s.provisions = policy }
