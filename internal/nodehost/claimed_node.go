package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ClaimStore supplies persisted acceptance and renewal under the awarded identity.
// RenewNodeClaim also keeps the claim's continuously live concurrency holders live.
type ClaimStore interface {
	NodeTransitionStore
	RenewNodeClaim(context.Context, store.NodeRef, store.Actor) (store.LeaseBeat, error)
}

// ClaimedNodeHost owns source and lease lifetime for an awarded remote job.
// The owner executes self-claimed jobs directly because its renewal covers them.
// Source contains the captured process base and live source authority;
// it must be built by the placement before entry acquisition starts.
type ClaimedNodeHost struct {
	Snapshots CapturedEnvironmentLoader
	Store     ClaimStore
	Lease     lease.Keeper
	Source    source.Options
	Acquire   func(context.Context, source.Spec, source.Options) (*source.Entry, error)
	Executor  NodeHost
	Mask      func(string) string
	Masks     *MaskRegistry
}

// Execute accepts only store-derived inputs, without a pre-acquired entry.
// A successful first renewal precedes every source, install and SDK operation.
func (h *ClaimedNodeHost) Execute(parent context.Context, input NodeInput) (err error) {
	if h.Store == nil || h.Executor == nil || h.Mask == nil {
		return errors.New("claimed node host requires store, executor and masking adapters")
	}
	defer func() { err = maskHostError(err, h.Mask) }()
	if input.Entry != nil || input.Accepted.RunID == "" || input.NodeID == "" || input.Authority.CapabilityPrefix == "" || input.Authority.Holder == "" || input.Generation < 1 || input.Attempt < 0 {
		return errors.New("claimed node host requires an unacquired, accepted award")
	}
	if h.Lease.Interval <= 0 || h.Lease.Lease <= h.Lease.Interval {
		return errors.New("claimed node host requires a positive renewal interval below its lease")
	}
	if err := h.Masks.initializeSeal(input.Accepted); err != nil {
		return err
	}
	if err := context.Cause(parent); err != nil {
		return err
	}
	spec, err := h.sourceSpec(parent, input)
	if err != nil {
		return err
	}
	if err := h.Masks.initializeAccepted(parent, h.Store, input.Accepted); err != nil {
		return err
	}
	actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix}, Holder: input.Authority.Holder, CapabilityPrefix: input.Authority.CapabilityPrefix, Generation: input.Generation}
	ref := store.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}
	keeper := h.Lease
	keeper.Renew = func(ctx context.Context) (lease.Beat, error) {
		beat, err := h.Store.RenewNodeClaim(ctx, ref, actor)
		return lease.Beat{CancelRequested: beat.CancelRequested, CancelReason: beat.CancelReason, Watched: beat.Watched, WatchedNodes: beat.WatchedNodes}, err
	}
	ctx, stop := parent, func() {}
	if !lease.Adopt(parent, lease.Identity{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID, Holder: actor.Holder, Generation: actor.Generation}, func() bool { return true }) {
		ctx, stop = startHostKeeper(parent, keeper)
	}
	defer stop()
	if err := context.Cause(ctx); err != nil {
		return h.setupFailure(ctx, input, err)
	}
	acquire := h.Acquire
	if acquire == nil {
		acquire = source.Acquire
	}
	var accepted runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &accepted); err != nil {
		return h.setupFailure(ctx, input, err)
	}
	options := h.Source
	options.Base, err = capturedBase(ctx, options.Base, h.Snapshots, accepted.Request.Request.Env)
	if err != nil {
		return h.setupFailure(ctx, input, err)
	}
	entry, err := acquire(ctx, spec, options)
	if entry != nil {
		defer func() {
			if closeErr := entry.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}()
	}
	if cause := context.Cause(ctx); cause != nil {
		return h.setupFailure(ctx, input, cause)
	}
	if err == nil {
		switch {
		case entry == nil:
			err = errors.New("source acquisition returned no entry")
		case entry.Engine.Identity != *spec.Engine:
			err = executorError(apierr.SpecDrift, "source engine differs from the accepted release")
		case spec.SHA != "" && !strings.EqualFold(entry.SHA, spec.SHA):
			err = executorError(apierr.SpecDrift, "source commit differs from the accepted commit")
		case entry.RepoKey != spec.RepoKey:
			err = executorError(apierr.SpecDrift, "source repository differs from the accepted repository")
		}
	}
	if err != nil {
		return h.setupFailure(ctx, input, err)
	}
	input.Entry, input.Engine = entry, entry.Engine
	err = h.Executor.Execute(ctx, input)
	return errors.Join(err, context.Cause(ctx))
}

func (h *ClaimedNodeHost) sourceSpec(ctx context.Context, input NodeInput) (source.Spec, error) {
	snapshot, err := h.Store.ReadSnapshot(ctx, input.Accepted.RunID)
	if err != nil {
		return source.Spec{}, err
	}
	accepted, err := ownerAccepted(snapshot.Run)
	if err != nil {
		return source.Spec{}, err
	}
	if !reflect.DeepEqual(accepted, input.Accepted) {
		return source.Spec{}, executorError(apierr.SpecDrift, "job award differs from persisted acceptance")
	}
	if input.Engine.Identity != snapshot.Run.Spec.Resolved.Engine {
		return source.Spec{}, executorError(apierr.SpecDrift, "job award differs from the accepted engine")
	}
	ref := store.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}
	row, err := h.Store.ReadNode(ctx, ref)
	if err != nil {
		return source.Spec{}, err
	}
	if row.Ref != ref || row.ClaimedBy != input.Authority.Holder || row.CapabilityPrefix != input.Authority.CapabilityPrefix || row.Generation != input.Generation || row.Attempt != input.Attempt {
		return source.Spec{}, executorError(apierr.LockHeld, "job award no longer holds the persisted claim")
	}
	constructed, err := NewNodeInput(accepted, row, input.Engine, input.GrantedRunner)
	if err != nil {
		return source.Spec{}, err
	}
	if constructed.SpecHash != input.SpecHash || !reflect.DeepEqual(constructed.Execution, input.Execution) {
		return source.Spec{}, executorError(apierr.SpecDrift, "job award differs from persisted execution settings")
	}
	return source.ForExecution(*snapshot.Run.Spec)
}

func (h *ClaimedNodeHost) setupFailure(ctx context.Context, input NodeInput, failure error) error {
	if cause := context.Cause(ctx); cause != nil {
		var cancelled *lease.CancelError
		if !errors.As(cause, &cancelled) {
			return errors.Join(failure, cause)
		}
	}
	code := apierr.Internal
	var structured *apierr.Error
	if errors.As(failure, &structured) {
		code = structured.Code
	}
	result := store.NodeResult{Outcome: store.NodeFailed, Reason: string(code), MaskedError: boundedNodeErrText(h.Mask(failure.Error()))}
	if ctx.Err() != nil {
		result.Outcome, result.Reason = store.NodeCancelled, string(apierr.ReasonCancelled)
	}
	result = recordNodeFailure(input.NodeID, result, h.Mask)
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
	defer cancel()
	_, err := h.Store.TransitionNode(reportCtx, store.NodeTransition{Ref: store.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}, Actor: executorActor(input), To: result.Outcome, Cause: store.CauseReport, SourceAttempt: &input.Attempt, Extra: store.TransitionExtra{Result: &result}})
	return errors.Join(context.Cause(ctx), &apierr.Error{Code: code, Message: result.MaskedError}, err)
}

func startHostKeeper(parent context.Context, keeper lease.Keeper) (context.Context, func()) {
	ready := make(chan struct{})
	var once sync.Once
	observe := keeper.Observe
	keeper.Observe = func(beat lease.Beat) {
		if observe != nil {
			observe(beat)
		}
		once.Do(func() { close(ready) })
	}
	ctx, stop := keeper.Run(parent)
	select {
	case <-ctx.Done():
	case <-ready:
	}
	return ctx, stop
}

var (
	_ NodeHost   = (*ClaimedNodeHost)(nil)
	_ ClaimStore = (*store.Tenant)(nil)
)
