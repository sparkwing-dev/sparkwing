package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// OwnerStore is the transactional boundary used by every run-host placement.
// Its methods are the Tenant contracts; hosts never append lifecycle events.
type OwnerStore interface {
	Reconciler
	ExpansionStore
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	AcceptPlan(context.Context, store.PlanAcceptance) (store.AcceptedPlan, error)
	TransitionRun(context.Context, store.RunTransition) (store.Applied, error)
	TransitionNode(context.Context, store.NodeTransition) (store.Applied, error)
	FinishRunAndUnit(context.Context, store.FinishRunRequest) (store.Applied, error)
	RenewOwner(context.Context, store.Actor) (store.LeaseBeat, error)
}

// OwnerPlanning supplies checkout facts and SDK planning without replacing intake.
type OwnerPlanning struct {
	Declarations runspec.CheckoutDeclarations
	Resolved     runspec.Resolved
	Planning     PlanningInput
	Functions    PlanningFunctions
	Resolve      func(nodeproto.PlanDoc) (runspec.Resolved, error)
}

// OwnerLayerResult either proposes a store transition or an authorized reuse candidate.
// A transition ends this scheduling pass; a source still needs atomic reconciliation.
type OwnerLayerResult struct {
	Transition *store.NodeTransition
	Source     *store.MaterializeSource
}

// OwnerHost coordinates the frozen lifecycle with source, SDK and placement adapters.
// Project and Decide default to the shared store functions. Wait must catch up
// against the snapshot's persisted state sequences before waiting for notification.
// Layer implements each owner-side policy named by LayerTable; it is required before
// dispatch so an unwired policy cannot silently permit execution.
// Source supplies the shared acquisition path. Acquire may replace it with a
// trusted adapter that uses RunInput's sealed request and persisted acceptance.
type OwnerHost struct {
	Snapshots CapturedEnvironmentLoader
	Store     OwnerStore
	Lease     lease.Keeper
	Source    source.Options
	Acquire   func(context.Context, RunInput) (*source.Entry, error)
	Prepare   func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error)
	Planner   *SDKPlanner
	Planning  PlanningInput
	Project   func(store.RunSnapshot) (store.ScheduleSnapshot, error)
	Decide    func(store.ScheduleSnapshot) store.ScheduleDecision
	Wait      func(context.Context, store.RunSnapshot) error
	WaitNode  WaitNodeChange
	Layer     func(context.Context, LayerSpec, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error)
	Local     func(store.NodeRow) bool
	Bind      func(context.Context, AcceptedRun, store.ReconciledNode, *source.Entry, StorageBinding) (NodeInput, error)
	Executor  NodeHost
	Expand    func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error)
	// Mask shares the source and SDK secret registry and protects returned errors.
	Mask func(string) string
	// Masks serializes accepted identifiers with the shared process service registry.
	Masks *MaskRegistry
}

var (
	_ RunHost    = (*OwnerHost)(nil)
	_ OwnerStore = (*store.Tenant)(nil)
)

// Run requires an initial successful renewal before acquiring source or resuming work.
// It joins every executor and renewal before returning, including error paths.
func (h *OwnerHost) Run(parent context.Context, input RunInput) (completion RunCompletion, err error) {
	if h.Store == nil || h.Wait == nil || h.Mask == nil {
		return completion, errors.New("run host requires store, state follower and masking adapters")
	}
	defer func() { err = maskHostError(err, h.Mask) }()
	if input.RunID == "" || input.RequestHash == "" || input.Owner.Kind != store.ActorRunHost || input.Owner.Owner.RunID != input.RunID {
		return completion, errors.New("run host requires a bound owner and sealed request")
	}
	if h.Lease.Interval <= 0 || h.Lease.Lease <= h.Lease.Interval {
		return completion, errors.New("run host requires a positive renewal interval below its lease")
	}
	ctx, stop := h.keepOwner(parent, input.Owner)
	defer stop()
	if err := context.Cause(ctx); err != nil {
		return completion, err
	}
	snapshot, err := h.Store.ReadSnapshot(ctx, input.RunID)
	if err != nil {
		return completion, err
	}
	if err := ownerRequestMatches(input, snapshot.Run); err != nil {
		return completion, err
	}
	if h.Masks != nil && snapshot.Run.Request.Request.SecretArgs.Set {
		for _, name := range snapshot.Run.Request.Request.SecretArgs.Value {
			if err := h.Masks.Register(snapshot.Run.Request.Request.RequestedArgs[name]); err != nil {
				return completion, err
			}
		}
	}
	if snapshot.Run.Spec != nil {
		persisted, err := ownerAccepted(snapshot.Run)
		if err != nil {
			return completion, err
		}
		if input.Accepted != nil && !reflect.DeepEqual(*input.Accepted, persisted) {
			return completion, errors.New("resume differs from persisted acceptance")
		}
		input.Accepted = &persisted
		if h.Masks != nil {
			namespace := persisted.Plan
			for _, row := range snapshot.Nodes {
				var job nodeproto.JobSpec
				if len(row.Spec) == 0 || json.Unmarshal(row.Spec, &job) != nil || job.ID != row.Ref.NodeID {
					return completion, errors.New("persisted mask namespace has an invalid job")
				}
				namespace.Jobs = append(namespace.Jobs, job)
			}
			if err := h.Masks.Accept(namespace, func() error { return nil }); err != nil {
				return completion, err
			}
		}
	} else if input.Accepted != nil {
		return completion, errors.New("resume has no persisted acceptance")
	}
	entry, err := h.acquire(ctx, input, snapshot.Run)
	if entry != nil {
		defer func() { err = errors.Join(err, entry.Close()) }()
	}
	if cause := context.Cause(ctx); cause != nil {
		err = errors.Join(err, cause)
	}
	if err != nil {
		return completion, h.preparationFailure(ctx, input, snapshot.Run, err)
	}
	if entry == nil {
		return completion, h.preparationFailure(ctx, input, snapshot.Run, errors.New("source acquisition returned no entry"))
	}
	accepted, err := h.accept(ctx, input, snapshot.Run, entry)
	if err != nil {
		return completion, err
	}
	if snapshot.Run.State == store.RunPending {
		_, err = h.Store.TransitionRun(ctx, store.RunTransition{RunID: input.RunID, To: store.RunRunning, Actor: input.Owner, Cause: store.RunStart})
		if err != nil {
			return completion, err
		}
	}
	return h.schedule(ctx, input, accepted, entry)
}

func (h *OwnerHost) acquire(ctx context.Context, input RunInput, row store.RunRow) (*source.Entry, error) {
	if h.Acquire != nil {
		return h.Acquire(ctx, input)
	}
	var spec source.Spec
	var err error
	if row.Spec == nil {
		spec, err = source.ForRequest(row.Request)
	} else {
		spec, err = source.ForExecution(*row.Spec)
	}
	if err != nil {
		return nil, err
	}
	options := h.Source
	options.Base, err = capturedBase(ctx, options.Base, h.Snapshots, row.Request.Request.Env)
	if err != nil {
		return nil, err
	}
	if options.Engines == nil && input.Engines != nil {
		options.Engines = input.Engines
	}
	return source.Acquire(ctx, spec, options)
}

func (h *OwnerHost) keepOwner(parent context.Context, actor store.Actor) (context.Context, func()) {
	keeper := h.Lease
	keeper.Renew = func(ctx context.Context) (lease.Beat, error) {
		beat, err := h.Store.RenewOwner(ctx, actor)
		return lease.Beat{CancelRequested: beat.CancelRequested, CancelReason: beat.CancelReason, Watched: beat.Watched, WatchedNodes: beat.WatchedNodes}, err
	}
	return startHostKeeper(parent, keeper)
}

func ownerRequestMatches(input RunInput, row store.RunRow) error {
	if row.ID != input.RunID || string(row.Request.Hash) != input.RequestHash {
		return errors.New("run host request does not match persisted intake")
	}
	var request runspec.Request
	if err := json.Unmarshal(input.SealedRequest, &request); err != nil {
		return fmt.Errorf("decode sealed request: %w", err)
	}
	if !reflect.DeepEqual(request, row.Request.Request) {
		return errors.New("run host cannot replace sealed intake fields")
	}
	if row.Owner == nil || *row.Owner != input.Owner.Owner {
		return errors.New("run host owner does not match persisted authority")
	}
	return nil
}

func (h *OwnerHost) accept(ctx context.Context, input RunInput, row store.RunRow, entry *source.Entry) (AcceptedRun, error) {
	if err := context.Cause(ctx); err != nil {
		return AcceptedRun{}, h.preparationFailure(ctx, input, row, err)
	}
	if row.Spec != nil {
		accepted, err := ownerAccepted(row)
		if err != nil {
			return AcceptedRun{}, err
		}
		if input.Accepted != nil && !reflect.DeepEqual(*input.Accepted, accepted) {
			return AcceptedRun{}, errors.New("resume differs from persisted acceptance")
		}
		return accepted, nil
	}
	if input.Accepted != nil {
		return AcceptedRun{}, errors.New("resume has no persisted acceptance")
	}
	prepared, err := h.prepare(ctx, input, row.Request, entry)
	if err != nil {
		return AcceptedRun{}, h.preparationFailure(ctx, input, row, err)
	}
	if input.PlanCache != nil {
		prepared.Planning.Cache = input.PlanCache
	}
	guards := prepared.Resolved.Guards
	if !guards.Passed && (len(guards.When)+len(guards.Unless) != 0 || guards.Reason != "") {
		prepared.Planning.Cache = nil
		prepared.Functions.Plan = func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"jobs":[]}`), nil }
	}
	if h.Masks != nil {
		prepared.Planning.Validation.Secrets = planningSecrets{prior: prepared.Planning.Validation.Secrets, masker: h.Masks.masker}
	}
	plan, err := PlanInitial(ctx, prepared.Planning, prepared.Functions)
	if err != nil {
		return AcceptedRun{}, h.preparationFailure(ctx, input, row, err)
	}
	if prepared.Resolve != nil {
		prepared.Resolved, err = prepared.Resolve(plan.Plan)
		if err != nil {
			return AcceptedRun{}, h.preparationFailure(ctx, input, row, err)
		}
	}
	if row.Request.Request.SecretArgs.Set {
		for _, name := range row.Request.Request.SecretArgs.Value {
			if !slices.Contains(prepared.Resolved.SecretArgs, name) {
				prepared.Resolved.SecretArgs = append(prepared.Resolved.SecretArgs, name)
			}
		}
	}
	var accepted store.AcceptedPlan
	commit := func() error {
		accepted, err = h.Store.AcceptPlan(ctx, store.PlanAcceptance{RunID: input.RunID, Actor: input.Owner, Seal: runspec.SealInput{
			Request: row.Request, Declarations: prepared.Declarations, Resolved: prepared.Resolved, Plan: plan.CanonicalPlan,
		}})
		return err
	}
	if h.Masks != nil {
		err = h.Masks.Accept(plan.Plan, commit)
	} else {
		err = commit()
	}
	if err != nil {
		return AcceptedRun{}, err
	}
	if !reflect.DeepEqual(accepted.Spec.Request, row.Request) || accepted.PlanHash != string(plan.PlanHash) {
		return AcceptedRun{}, errors.New("acceptance changed sealed request or planned declarations")
	}
	row.Spec, row.Plan, row.PlanHash = &accepted.Spec, accepted.Plan, accepted.PlanHash
	return ownerAccepted(row)
}

func (h *OwnerHost) preparationFailure(ctx context.Context, input RunInput, row store.RunRow, failure error) error {
	// safety: accepted topology may have live remote work; only unaccepted setup can finish here.
	if row.Spec != nil {
		return failure
	}
	state, reason := store.RunFailed, string(apierr.PlanFailed)
	message := "run preparation failed"
	if cause := context.Cause(ctx); cause != nil {
		var cancelled *lease.CancelError
		if !errors.As(cause, &cancelled) {
			return errors.Join(failure, cause)
		}
		state, reason = store.RunCancelled, string(apierr.ReasonCancelled)
		message = "cancellation requested"
	}
	// safety: setup errors can contain credentials before describe establishes masking; persist only the stage.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
	defer cancel()
	_, err := h.Store.FinishRunAndUnit(finishCtx, store.FinishRunRequest{
		Unit: input.Owner.Owner,
		Transition: store.RunTransition{
			RunID: input.RunID, Actor: input.Owner,
			To: state, Cause: store.RunFinish, Reason: reason, MaskedError: message,
		},
	})
	return errors.Join(failure, err)
}

func ownerAccepted(row store.RunRow) (AcceptedRun, error) {
	if row.Spec == nil || row.Spec.Hash == "" || row.PlanHash == "" || string(row.Spec.PlanHash) != row.PlanHash {
		return AcceptedRun{}, errors.New("store returned an incomplete run seal")
	}
	result := AcceptedRun{RunID: row.ID, SpecHash: string(row.Spec.Hash), PlanHash: row.PlanHash}
	var err error
	result.Spec, err = json.Marshal(row.Spec)
	if err != nil {
		return AcceptedRun{}, err
	}
	if err = json.Unmarshal(row.Plan, &result.Plan); err != nil {
		return AcceptedRun{}, fmt.Errorf("decode accepted plan: %w", err)
	}
	return result, nil
}
