package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func checkPublishedLogWriterTx(ctx context.Context, tx *storeTx, team Team, subject blob.LogSubject, c capability.Capability) error {
	if subject.Kind != blob.LogJobAttempt || subject.Attempt == nil || c.Binding.RunID != subject.RunID {
		return holderRefusal()
	}
	// safety: publication has no execution lease; the imported terminal attempt supplies the fence.
	authority := blob.Authority{CapabilityRef: c.Prefix, Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: *subject.Attempt}}
	if _, err := resolveBlobAuthorityTx(ctx, tx, team, authority, capability.DataWrite); err != nil {
		return err
	}
	a := subject.Attempt
	var imported int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM node_attempts WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=? AND finished_at IS NOT NULL", team, subject.RunID, a.NodeID, a.Attempt, a.Generation).Scan(&imported); err != nil {
		return err
	}
	if imported != 1 {
		return holderRefusal()
	}
	return nil
}
