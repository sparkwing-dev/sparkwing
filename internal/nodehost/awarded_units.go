package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Plan accepts a controller-owned run's plan under its planning lease and leaves scheduling to the controller.
func (h *OwnerHost) Plan(parent context.Context, input RunInput) (accepted AcceptedRun, err error) {
	if err := h.unitReady(input); err != nil {
		return accepted, err
	}
	defer func() { err = maskHostError(errors.Join(err, context.Cause(parent)), h.Mask) }()
	owner := input.Owner.Owner
	if owner.Kind != store.OwnerTrigger || owner.RunID != input.RunID || owner.Holder == "" || owner.Generation <= 0 {
		return accepted, errors.New("plan unit requires its planning lease")
	}
	ctx, stop := h.keepUnit(parent, lease.Identity{Kind: capability.UnitPlan, RunID: input.RunID, Holder: owner.Holder, Generation: owner.Generation}, input.Owner, func() bool { return true })
	defer stop()
	if err := context.Cause(ctx); err != nil {
		return accepted, err
	}
	snapshot, err := h.Store.ReadSnapshot(ctx, input.RunID)
	if err != nil {
		return accepted, err
	}
	if err := ownerRequestMatches(input, snapshot.Run); err != nil {
		return accepted, err
	}
	if err := h.Masks.initialize(snapshot.Run.Request, nil, nil, nil); err != nil {
		return accepted, err
	}
	entry, err := h.acquire(ctx, input, snapshot.Run)
	if entry != nil {
		defer func() { err = errors.Join(err, entry.Close()) }()
	}
	if err = errors.Join(err, context.Cause(ctx)); err != nil {
		return accepted, h.preparationFailure(ctx, input, snapshot.Run, err)
	}
	if entry == nil {
		return accepted, h.preparationFailure(ctx, input, snapshot.Run, errors.New("plan acquisition returned no entry"))
	}
	return h.accept(ctx, input, snapshot.Run, entry)
}

// Evaluate submits a source expansion under its independent eval lease.
func (h *OwnerHost) Evaluate(parent context.Context, input RunInput, sourceID string, generation int64, fence capability.LeaseRef) (err error) {
	if err := h.unitReady(input); err != nil {
		return err
	}
	if input.Accepted == nil || sourceID == "" || generation < 0 || fence.Kind != capability.UnitEval || fence.ID == "" || fence.Holder == "" || fence.Generation <= 0 || h.Expand == nil {
		return errors.New("eval unit requires accepted source, expansion lease and evaluator")
	}
	defer func() { err = maskHostError(errors.Join(err, context.Cause(parent)), h.Mask) }()
	if err := h.Masks.initializeSeal(*input.Accepted); err != nil {
		return err
	}
	ctx, stop := h.keepUnit(parent, lease.Identity{Kind: capability.UnitEval, RunID: input.RunID, NodeID: sourceID, Holder: fence.Holder, Generation: fence.Generation}, input.Owner, func() bool { return true })
	defer stop()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	snapshot, err := h.Store.ReadSnapshot(ctx, input.RunID)
	if err != nil {
		return err
	}
	accepted, err := ownerAccepted(snapshot.Run)
	if err != nil || !reflect.DeepEqual(accepted, *input.Accepted) || string(snapshot.Run.Request.Hash) != input.RequestHash {
		return errors.Join(err, errors.New("eval award differs from persisted acceptance"))
	}
	if err := h.Masks.initialize(snapshot.Run.Request, &snapshot.Run.Spec.Resolved, &accepted.Plan, snapshot.Nodes); err != nil {
		return err
	}
	var row store.NodeRow
	for _, current := range snapshot.Nodes {
		if current.Ref == (controllerwire.NodeRef{RunID: input.RunID, NodeID: sourceID}) {
			row = current
			break
		}
	}
	if row.Ref.NodeID == "" || row.Generation != generation || row.Status != store.StatusDone || row.Outcome != store.NodeSuccess && row.Outcome != store.NodeMemoized || row.Expansion != store.ExpansionExpected {
		return errors.New("eval source is outside the awarded expansion")
	}
	entry, err := h.acquire(ctx, input, snapshot.Run)
	if entry != nil {
		defer func() { err = errors.Join(err, entry.Close()) }()
	}
	if cause := context.Cause(ctx); cause != nil {
		return errors.Join(err, cause)
	}
	if err != nil || entry == nil {
		return h.commitExpansion(ctx, row, input.Owner, nil, &store.ExpansionFailure{Code: apierr.PlanFailed, MaskedError: "evaluation source acquisition failed"})
	}
	body, failure, err := h.Expand(ctx, ownerEvaluation(input, accepted, entry, row), row, input.Storage)
	if err != nil {
		return err
	}
	return h.commitExpansion(ctx, row, input.Owner, body, failure)
}

func (h *OwnerHost) unitReady(input RunInput) error {
	if h.Store == nil || h.Mask == nil || h.Renew == nil || h.Lease.Interval <= 0 || h.Lease.Lease <= h.Lease.Interval || input.RunID == "" || input.RequestHash == "" || input.Owner.Kind != store.ActorRunHost {
		return errors.New("awarded unit requires state, renewal, masking and sealed input")
	}
	return nil
}

func (h *OwnerHost) commitExpansion(ctx context.Context, row store.NodeRow, actor store.Actor, body json.RawMessage, failure *store.ExpansionFailure) error {
	if (len(body) == 0) == (failure == nil) {
		return errors.New("generator must return declarations or a structured failure")
	}
	commit := func() error {
		_, err := h.Store.AcceptExpansion(ctx, store.ExpansionSubmission{Source: row.Ref, SourceGeneration: row.Generation, Actor: actor, Body: body, Failure: failure})
		return err
	}
	if h.Masks != nil && failure == nil {
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(body, &plan); err != nil {
			return err
		}
		return h.Masks.Accept(plan, commit)
	}
	return commit()
}
