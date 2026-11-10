package nodehost

import (
	"context"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func waitClaimCancellation(ctx context.Context, read func(context.Context, string) (store.RunSnapshot, error), ref runspec.NodeRef, actor runstate.Actor) *lease.CancelError {
	// safety: Cancellation must reach execution before the next lease renewal.
	subscription, err := localStateSubscription(ctx, string(ref.RunID))
	if err != nil {
		return nil
	}
	defer func() {
		if err := subscription.Close(); err != nil {
			slog.WarnContext(ctx, "cancellation state subscription cleanup failed", "error", err)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-subscription.Changed:
			if !ok {
				return nil
			}
		}
		snapshot, err := read(ctx, string(ref.RunID))
		if err != nil || snapshot.Run.ID != string(ref.RunID) {
			// safety: A failed observation must not revoke authority or replace lease outage handling.
			continue
		}
		for _, row := range snapshot.Nodes {
			if row.Ref != ref || row.Generation != actor.Generation || row.ClaimedBy != actor.Holder || row.CapabilityPrefix != actor.CapabilityPrefix {
				continue
			}
			if row.CancelRequestedAt != nil || snapshot.Run.CancelRequestedAt != nil {
				reason := snapshot.Run.CancelReason
				if row.CancelRequestedAt != nil {
					reason = row.CancelReason
				}
				if reason == "" {
					reason = "cancel_requested"
				}
				return &lease.CancelError{Reason: reason}
			}
		}
	}
}
