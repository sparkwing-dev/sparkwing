package nodehost

import (
	"context"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func persistedNodeCancellation(ctx context.Context, readSnapshot func(context.Context, string) (store.RunSnapshot, error), input NodeInput, failure error) *lease.CancelError {
	if failure == nil || ctx.Err() != nil {
		return nil
	}
	// safety: a sensitive startup request can observe cancellation before the next lease renewal.
	snapshot, err := readSnapshot(ctx, input.Accepted.RunID)
	if err != nil {
		return nil
	}
	for _, row := range snapshot.Nodes {
		if row.Ref.NodeID == runspec.NodeID(input.NodeID) && row.Generation == input.Generation && row.Attempt == input.Attempt &&
			row.ClaimedBy == input.Authority.Holder && row.CapabilityPrefix == input.Authority.CapabilityPrefix &&
			(snapshot.Run.CancelRequestedAt != nil || row.CancelRequestedAt != nil) {
			return &lease.CancelError{Reason: string(hosterr.ReasonCancelled)}
		}
	}
	return nil
}
