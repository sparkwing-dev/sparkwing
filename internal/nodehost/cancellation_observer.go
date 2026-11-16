package nodehost

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

const claimCancellationInterval = 3 * time.Second

func waitClaimCancellation(ctx context.Context, read func(context.Context, runspec.NodeRef) (runstate.NodeRow, error), ref runspec.NodeRef, actor runstate.Actor) *lease.CancelError {
	// perf: Point reads at a fixed cadence bound cancellation traffic per awarded claim.
	ticker := time.NewTicker(claimCancellationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		row, err := read(ctx, ref)
		if err != nil {
			// safety: A failed observation must not revoke authority or replace lease outage handling.
			continue
		}
		if row.Ref != ref || row.Generation != actor.Generation || row.ClaimedBy != actor.Holder || row.CapabilityPrefix != actor.CapabilityPrefix {
			continue
		}
		if row.CancelRequestedAt != nil {
			reason := row.CancelReason
			if reason == "" {
				reason = "cancel_requested"
			}
			return &lease.CancelError{Reason: reason}
		}
	}
}
