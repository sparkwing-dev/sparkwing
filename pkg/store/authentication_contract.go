package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ReadAuthentication reads presentation identity, full ancestry, membership and
// target liveness in one committed snapshot with the store clock. Evidence selects
// records but grants no authority. No reusable presentation enters persistence.
func (s *Store) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	panic("unimplemented: c1-authority")
}

var _ capability.AuthenticationReader = (*Store)(nil)
