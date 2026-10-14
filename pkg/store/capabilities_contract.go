package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Issue is the only persistent capability constructor. It participates in the locked claim,
// intake or exchange transaction and uses the store clock for ancestry, expiry and fences.
// The caller must roll back the transaction on error and expose Raw only after commit.
func (s *Store) Issue(ctx context.Context, tx *storeTx, request capability.IssueRequest) (capability.Issued, error) {
	panic("unimplemented: c2-credentials")
}
