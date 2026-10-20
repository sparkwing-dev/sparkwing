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
	_, err := resolveBlobAuthorityTx(ctx, tx, team, authority, capability.DataWrite)
	return err
}
