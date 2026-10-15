package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ProvisionPolicy verifies the bootstrap gate, enrollment or Actions evidence in the issuing transaction.
type ProvisionPolicy interface {
	AuthorizeProvision(context.Context, capability.AuthorityResolver, capability.ProvisionSpec) error
}

// SetProvisionPolicy installs trusted origin verification before concurrent store use.
func (s *Store) SetProvisionPolicy(policy ProvisionPolicy) { s.provisions = policy }
