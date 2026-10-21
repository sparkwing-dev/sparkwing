package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

var errBurstProgress = errors.New("burst job changed while acquiring machine capacity")

func (h *OwnerHost) executeBurst(ctx context.Context, run RunInput, accepted AcceptedRun, entry *source.Entry, request store.ReconcileRequest) error {
	for {
		before, err := h.Store.ReadSnapshot(ctx, accepted.RunID)
		if err != nil {
			return err
		}
		row, ok := ownerNode(before, request.Ref.NodeID)
		if !ok {
			return errors.New("burst admission lost its job")
		}
		if row.Status == store.StatusDone {
			return nil
		}
		if row.ClaimedBy != "" && row.OwnerCapabilityPrefix != run.Owner.CapabilityPrefix {
			if err := h.WaitNode(ctx, row.Ref, row.StateSeq); err != nil {
				return err
			}
			continue
		}
		if row.OwnerExecution != nil && !row.OwnerExecution.Allowed {
			if !hasLocalConcurrency(row.Execution.Concurrency) {
				return nil
			}
			if err := h.Wait(ctx, before); err != nil {
				return err
			}
			continue
		}
		dispatched := false
		err = h.reserveBurst(ctx, accepted, row, func(ctx context.Context, resources wingwire.HostResources) error {
			claimRequest := request
			claimRequest.Reserved = &match.Resources{Cores: resources.Cores, MemoryBytes: resources.MemoryBytes}
			result, err := FollowReconciliation(ctx, h.Store, claimRequest, func(context.Context, controllerwire.NodeRef, int64) error { return errBurstProgress })
			if err != nil {
				return err
			}
			if result.Kind == store.Execute {
				return h.executeAward(ctx, run, accepted, entry, result)
			}
			dispatched = result.Kind == store.Dispatch
			return nil
		})
		if errors.Is(err, errBurstProgress) && waitFailure(err, errBurstProgress) == nil {
			continue
		}
		var refused *store.ErrIllegalTransition
		if errors.As(err, &refused) && refused.Reason == store.ReasonHolderRights {
			// safety: a claim may commit before the state follower observes it.
			current, readErr := h.Store.ReadSnapshot(ctx, accepted.RunID)
			if readErr != nil {
				return errors.Join(err, readErr)
			}
			latest, ok := ownerNode(current, row.Ref.NodeID)
			if ok && (latest.Generation != row.Generation || latest.StateSeq > row.StateSeq) {
				continue
			}
		}
		if err != nil || !dispatched || !hasLocalConcurrency(row.Execution.Concurrency) {
			return err
		}
		if err := h.Wait(ctx, before); err != nil {
			return err
		}
	}
}

func (h *OwnerHost) reserveBurst(parent context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context, wingwire.HostResources) error) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	watchCtx, stop := context.WithCancel(ctx)
	changed := make(chan error, 1)
	go func() {
		err := h.WaitNode(watchCtx, row.Ref, row.StateSeq)
		if err == nil {
			cancel(errBurstProgress)
		} else if watchCtx.Err() == nil {
			cancel(err)
		}
		changed <- err
	}()
	joined := false
	defer func() {
		stop()
		if !joined {
			<-changed
		}
	}()
	actor := store.Actor{}
	if h.executionAdmission.capacity != nil {
		actor = h.executionAdmission.capacity.Owner
	}
	return h.executionAdmission.withNode(ctx, accepted, row, actor, func(ctx context.Context, resources wingwire.HostResources) error {
		stop()
		err := <-changed
		joined = true
		if err != nil && waitFailure(err, context.Canceled) != nil {
			return err
		}
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return body(ctx, resources)
	})
}
