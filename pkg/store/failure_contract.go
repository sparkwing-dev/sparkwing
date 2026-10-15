package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// ReadFailure returns the persisted masked failure and stage for a declared recovery source.
// The transaction checks the reader's recovery edge and exact source generation.
// A terminal status without recorded failure is not a synthetic SDK failure.
func (t *Tenant) ReadFailure(ctx context.Context, source NodeRef, generation int64, actor Actor) (*nodeproto.JobFailure, error) {
	panic("unimplemented: c1-data")
}
