package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Plan accepts a controller-owned run's plan under its planning lease and leaves scheduling to the controller.
func (h *OwnerHost) Plan(parent context.Context, input RunInput) (accepted AcceptedRun, err error) {
	if err := h.unitReady(input); err != nil {
		return accepted, err
	}
	defer func() { err = maskHostError(errors.Join(err, context.Cause(parent)), h.Masker) }()
	owner := input.Owner.Owner
	if owner.Kind != runstate.OwnerTrigger || owner.RunID != runspec.RunID(input.RunID) || owner.Holder == "" || owner.Generation <= 0 {
		return accepted, errors.New("plan unit requires its planning lease")
	}
	ctx, stop := h.keepUnit(parent, input.Owner, func() bool { return true })
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
	bound := *h
	h = &bound
	if err := h.unitReady(input); err != nil {
		return err
	}
	if input.Accepted == nil || sourceID == "" || generation < 0 || fence.Kind != capability.UnitEval || fence.ID == "" || fence.Holder == "" || fence.Generation <= 0 {
		return errors.New("eval unit requires accepted source, expansion lease and evaluator")
	}
	defer func() { err = maskHostError(errors.Join(err, context.Cause(parent)), h.Masker) }()
	if err := h.Masks.initializeSeal(*input.Accepted); err != nil {
		return err
	}
	ctx, stop := h.keepUnit(parent, input.Owner, func() bool { return true })
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
	if err := h.Masks.initializeSnapshot(snapshot, accepted); err != nil {
		return err
	}
	var row runstate.NodeRow
	for _, current := range snapshot.Nodes {
		if current.Ref == (runspec.NodeRef{RunID: runspec.RunID(input.RunID), NodeID: runspec.NodeID(sourceID)}) {
			row = current
			break
		}
	}
	var job nodeproto.JobSpec
	if json.Unmarshal(row.Spec, &job) != nil {
		return errors.New("evaluation requires accepted declaration")
	}
	if job.Approval != nil {
		if row.Generation != runspec.ClaimGeneration(generation) || row.Status != runstate.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "" || row.Outcome != "" {
			return errors.New("approval evaluation requires its unreleased source")
		}
		return h.evaluateApproval(ctx, input, accepted, row, snapshot.Run)
	}
	if h.Expand == nil {
		return errors.New("source expansion requires evaluator")
	}
	if row.Ref.NodeID == "" || row.Generation != runspec.ClaimGeneration(generation) || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeSuccess && row.Outcome != runstate.NodeMemoized || row.Expansion != runstate.ExpansionExpected {
		return errors.New("eval source is outside the awarded expansion")
	}
	work := func(ctx context.Context) (err error) {
		entry, err := h.acquire(ctx, input, snapshot.Run)
		if entry != nil {
			defer func() { err = errors.Join(err, entry.Close()) }()
		}
		if cause := context.Cause(ctx); cause != nil {
			return errors.Join(err, cause)
		}
		if err != nil || entry == nil {
			return h.commitExpansion(ctx, row, input.Owner, nil, runspec.SecretRegistration{}, &store.ExpansionFailure{Code: apierr.PlanFailed, MaskedError: "evaluation source acquisition failed"})
		}
		body, registration, failure, err := h.Expand(ctx, ownerEvaluation(input, accepted, entry, row), row, input.Storage)
		if err != nil {
			return err
		}
		return h.commitExpansion(ctx, row, input.Owner, body, registration, failure)
	}
	if h.executionAdmission.compute != nil {
		return h.executionAdmission.compute.node(ctx, accepted, row, work)
	}
	return work(ctx)
}

func (h *OwnerHost) unitReady(input RunInput) error {
	if h.Store == nil || h.Masker == nil || h.Renew == nil || h.Lease.Interval <= 0 || h.Lease.Lease <= h.Lease.Interval || input.RunID == "" || input.RequestHash == "" || input.Owner.Kind != runstate.ActorRunHost {
		return errors.New("awarded unit requires state, renewal, masking and sealed input")
	}
	return nil
}

func (h *OwnerHost) commitExpansion(ctx context.Context, row runstate.NodeRow, actor runstate.Actor, body json.RawMessage, registration runspec.SecretRegistration, failure *store.ExpansionFailure) error {
	if (len(body) == 0) == (failure == nil) {
		return errors.New("generator must return declarations or a structured failure")
	}
	commit := func() error {
		_, err := h.Store.AcceptExpansion(ctx, store.ExpansionSubmission{Source: row.Ref, SourceGeneration: row.Generation, Actor: actor, Body: body, Registration: registration, Failure: failure})
		return err
	}
	if h.Masks != nil && failure == nil {
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(body, &plan); err != nil {
			return err
		}
		return h.Masks.acceptPlan(plan, commit)
	}
	return commit()
}

func (h *OwnerHost) evaluateApproval(ctx context.Context, input RunInput, accepted AcceptedRun, row runstate.NodeRow, run store.RunRow) (err error) {
	if h.SkipPolicy == nil {
		return errors.New("approval evaluation requires predicate policy")
	}
	entry, err := h.acquire(ctx, input, run)
	if entry != nil {
		defer func() { err = errors.Join(err, entry.Close()) }()
	}
	var result ownerLayerResult
	if err != nil || entry == nil {
		if context.Cause(ctx) != nil {
			return errors.Join(err, context.Cause(ctx))
		}
		result.Transition = store.OwnerPolicyNode{Outcome: runstate.NodeFailed, OwnerPolicy: &runstate.OwnerPolicyResult{Kind: runstate.PolicyApprovalFail, Generation: row.Generation}, Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: string(apierr.PlanFailed), MaskedError: "approval evaluation source acquisition failed"}}
	} else {
		result, err = h.SkipPolicy(ctx, ownerEvaluation(input, accepted, entry, row), row, input.Storage)
		if err != nil {
			return err
		}
	}
	var command store.NodeCommand = store.OpenApprovalNode{Ref: row.Ref, Actor: input.Owner}
	if result.Transition != nil {
		command = store.BindNodeCommand(result.Transition, row.Ref, input.Owner)
	}
	_, err = h.Store.TransitionNode(ctx, command)
	return err
}
