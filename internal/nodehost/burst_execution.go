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
		row, ok := ownerNode(before, string(request.Ref.NodeID))
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
	a := h.claimAdmission
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	options := spec.Resolved.Settings.Options
	request := wingwire.AdmissionRequest{
		RunID:        wingwire.NodeHostParticipant(a.holderRunID(accepted.RunID), string(row.Ref.NodeID)),
		DisplayRunID: accepted.RunID + "/" + string(row.Ref.NodeID), Pipeline: spec.Request.Request.Pipeline,
		Resources: localResources(row.Execution.Resources.Value), Priority: options.Priority.Value, Class: options.AdmissionClass.Value,
	}
	return a.hold(ctx, row.Ref, request, func(ctx context.Context, held Lease) error {
		stop()
		err := <-changed
		joined = true
		if err != nil && waitFailure(err, context.Canceled) != nil {
			return err
		}
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return body(ctx, held.Resources())
	})
}
