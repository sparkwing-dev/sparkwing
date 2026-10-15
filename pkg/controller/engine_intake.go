package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunTransactions is the engine transaction boundary. Persistence owns validation,
// clocks, replay, and transactional authority rechecks on each mutation.
type RunTransactions interface {
	AdmitRun(context.Context, store.IntakeRequest) (store.AdmissionResult, error)
	ReadRun(context.Context, string) (store.RunRow, error)
	ReadNode(context.Context, store.NodeRef) (store.NodeRow, error)
	AcceptPlan(context.Context, store.PlanAcceptance) (store.AcceptedPlan, error)
	AcceptExpansion(context.Context, store.ExpansionSubmission) (store.AcceptedExpansion, error)
	TransitionNode(context.Context, store.NodeTransition) (store.Applied, error)
	TransitionRun(context.Context, store.RunTransition) (store.Applied, error)
	FinishRunAndUnit(context.Context, store.FinishRunRequest) (store.Applied, error)
	RenewOwner(context.Context, store.Actor) (store.LeaseBeat, error)
	RenewNodeClaim(context.Context, store.NodeRef, store.Actor) (store.LeaseBeat, error)
}

func (s *EngineController) runTransactions(ctx context.Context, team string) (RunTransactions, error) {
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	if s.ResolveTenant != nil {
		tenant, err := s.ResolveTenant(ctx, store.Team(team))
		if err == nil && tenant == nil {
			return nil, missingEndpointPolicy()
		}
		return tenant, err
	}
	if s.Store == nil {
		return nil, missingEndpointPolicy()
	}
	tenant, err := s.Store.ForTeam(ctx, store.Team(team))
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return nil, &apierr.Error{Code: apierr.NotFound, Message: "team is not registered"}
	}
	return tenant, err
}

// IntakeForStore preserves tagged authority and the idempotency key. API input
// cannot assert an actor or verified App facts. Webhook intake supplies its
// authenticated delivery ID through the separate store boundary.
func IntakeForStore(a capability.Authorized, body controllerwire.CreateRunRequest) (store.IntakeRequest, error) {
	p := a.Principal
	if p.Authority.ID == "" || a.Target.Team == "" {
		return store.IntakeRequest{}, missingEndpointPolicy()
	}
	request := body.Request
	request.Team = a.Target.Team
	request.Actor = runspec.Actor{Kind: string(p.Capability.Subject.Kind), ID: p.Capability.Subject.ID}
	// safety: only the verified webhook intake may attach App provenance.
	request.Git.App = runspec.Optional[runspec.AppFacts]{}
	request.Lineage.Retrier = runspec.Optional[runspec.Actor]{}
	if request.Lineage.RetryOf != "" {
		request.Lineage.Retrier = runspec.Optional[runspec.Actor]{Set: true, Value: request.Actor}
	}
	return store.IntakeRequest{Authority: p.Authority, CapabilityPrefix: principalCapabilityPrefix(p), Request: request, IdempotencyKey: body.IdempotencyKey}, nil
}

// CreateRun calls the sole intake transaction and returns its committed replay
// result and private one-time here bearer to the requesting host.
func (s *EngineController) CreateRun(ctx context.Context, authorized capability.Authorized, body controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
	request, err := IntakeForStore(authorized, body)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	tenant, err := s.runTransactions(ctx, authorized.Target.Team)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	admitted, err := tenant.AdmitRun(ctx, request)
	if err != nil {
		return controllerwire.CreateRunResponse{}, engineLookupError(err)
	}
	return controllerwire.CreateRunResponse{
		RunID: admitted.RunID, Number: admitted.Number, Request: admitted.Request,
		HereBearer: string(admitted.HereBearer), Replayed: admitted.Replayed,
	}, nil
}

// ExpansionForStore derives the source and actor from authorized resource facts,
// compares its immutable generation, and requires Body XOR Failure. Failure code
// and masked message enter the same transaction as a successful expansion.
func ExpansionForStore(a capability.Authorized, body controllerwire.ExpansionSubmission) (store.ExpansionSubmission, error) {
	if a.Target.RunID == "" || a.Target.NodeID == "" || a.Target.UnitSourceGeneration <= 0 || a.Principal.Authority.ID == "" {
		return store.ExpansionSubmission{}, missingEndpointPolicy()
	}
	if body.SourceGeneration <= 0 {
		return store.ExpansionSubmission{}, invalidEngineBody("source_generation must be positive")
	}
	if body.SourceGeneration != a.Target.UnitSourceGeneration || a.Binding.Generation != a.Target.UnitSourceGeneration {
		return store.ExpansionSubmission{}, &apierr.Error{Code: apierr.LockHeld, Message: "expansion source generation changed"}
	}
	if (len(body.Body) > 0) == (body.Failure != nil) {
		return store.ExpansionSubmission{}, invalidEngineBody("expansion requires exactly one body or failure")
	}
	actor, err := expansionActor(a)
	if err != nil {
		return store.ExpansionSubmission{}, err
	}
	request := store.ExpansionSubmission{
		Source: store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}, SourceGeneration: body.SourceGeneration, Actor: actor,
	}
	if body.Failure != nil {
		if strings.TrimSpace(string(body.Failure.Code)) == "" || int64(len(body.Failure.Message)) > limits.ErrorBytes {
			return store.ExpansionSubmission{}, invalidEngineBody("expansion failure requires a code and bounded masked message")
		}
		request.Failure = &store.ExpansionFailure{Code: body.Failure.Code, MaskedError: body.Failure.Message}
	} else {
		var document struct {
			Jobs []json.RawMessage `json:"jobs"`
		}
		if err := json.Unmarshal(body.Body, &document); err != nil || document.Jobs == nil {
			return store.ExpansionSubmission{}, invalidEngineBody("expansion body requires an explicit jobs array")
		}
		request.Body = append(json.RawMessage(nil), body.Body...)
	}
	return request, nil
}

// AcceptExpansion submits success or structured failure through C1's single
// expansion transaction and returns only its persisted topology and replay result.
func (s *EngineController) AcceptExpansion(ctx context.Context, authorized capability.Authorized, body controllerwire.ExpansionSubmission) (controllerwire.ExpansionAccepted, error) {
	request, err := ExpansionForStore(authorized, body)
	if err != nil {
		return controllerwire.ExpansionAccepted{}, err
	}
	tenant, err := s.runTransactions(ctx, authorized.Target.Team)
	if err != nil {
		return controllerwire.ExpansionAccepted{}, err
	}
	accepted, err := tenant.AcceptExpansion(ctx, request)
	if err != nil {
		return controllerwire.ExpansionAccepted{}, engineLookupError(err)
	}
	return expansionResponse(accepted)
}

func expansionActor(a capability.Authorized) (store.Actor, error) {
	lease := a.Binding.Expansion
	if lease == nil || lease.ID == "" || lease.Holder == "" || lease.Generation <= 0 {
		return store.Actor{}, missingEndpointPolicy()
	}
	kind := store.OwnerTrigger
	switch lease.Kind {
	case capability.UnitTrigger:
	case capability.UnitEval:
		kind = store.OwnerEval
	default:
		return store.Actor{}, missingEndpointPolicy()
	}
	return store.Actor{
		Authority: a.Principal.Authority, Kind: store.ActorRunHost, CapabilityPrefix: a.Principal.Capability.Prefix,
		Holder: lease.Holder, Generation: lease.Generation,
		Owner: store.OwnerLeaseRef{
			Kind: kind, Name: lease.ID, RunID: a.Target.RunID,
			SourceNodeID: a.Target.NodeID, SourceGeneration: a.Target.UnitSourceGeneration, Holder: lease.Holder, Generation: lease.Generation,
		},
	}, nil
}

func expansionResponse(accepted store.AcceptedExpansion) (controllerwire.ExpansionAccepted, error) {
	response := controllerwire.ExpansionAccepted{
		SourceGeneration: accepted.SourceGeneration, State: string(accepted.State), BodyHash: accepted.BodyHash,
		Jobs: make([]controllerwire.ExpandedJob, 0, len(accepted.Nodes)), Replayed: accepted.Replayed,
	}
	for _, node := range accepted.Nodes {
		var job nodeproto.JobSpec
		if err := json.Unmarshal(node.Spec, &job); err != nil || job.ID == "" || node.SpecHash == "" {
			return controllerwire.ExpansionAccepted{}, errors.New("invalid persisted expansion job")
		}
		response.Jobs = append(response.Jobs, controllerwire.ExpandedJob{Job: job, SpecHash: node.SpecHash, Execution: node.Execution})
	}
	return response, nil
}

// AcceptPlan preserves author declarations and loads the first seal from the
// tenant. C1 revalidates the proposal against that persisted request and ceilings
// in its acceptance transaction. The adapter supplies no acceptance clock.
func (s *EngineController) AcceptPlan(ctx context.Context, a capability.Authorized, body controllerwire.PlanSubmission) (controllerwire.PlanAccepted, error) {
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return controllerwire.PlanAccepted{}, err
	}
	run, err := tenant.ReadRun(ctx, a.Target.RunID)
	if err != nil {
		return controllerwire.PlanAccepted{}, engineLookupError(err)
	}
	if run.ID != a.Target.RunID || string(run.Team) != a.Target.Team || run.Request.RunID == "" {
		return controllerwire.PlanAccepted{}, missingEndpointPolicy()
	}
	accepted, err := tenant.AcceptPlan(ctx, store.PlanAcceptance{
		RunID: a.Target.RunID, Actor: engineActor(a),
		Seal: runspec.SealInput{Declarations: body.Declarations, Request: run.Request, Resolved: body.Resolved, Plan: body.Plan},
	})
	if err != nil {
		return controllerwire.PlanAccepted{}, engineLookupError(err)
	}
	return controllerwire.PlanAccepted{
		Spec: accepted.Spec, Plan: accepted.Plan, PlanHash: runspec.Hash(accepted.PlanHash), Replayed: accepted.Replayed, Notices: []engine.Notice{},
	}, nil
}

func engineActor(a capability.Authorized) store.Actor {
	b := a.Binding
	actor := store.Actor{
		Authority: a.Principal.Authority, Kind: store.ActorRunHost,
		CapabilityPrefix: principalCapabilityPrefix(a.Principal), Holder: b.Holder, Generation: b.Generation,
	}
	if a.Access.Credential == capability.CredentialPublisher {
		actor.Kind = store.ActorOperator
	}
	if a.Principal.Capability.Unit == capability.UnitPlan || a.Principal.Capability.Unit == capability.UnitNode {
		actor.Kind = store.ActorHolder
	}
	if a.Principal.Capability.Unit == capability.UnitTrigger {
		actor.Owner = store.OwnerLeaseRef{
			Kind: store.OwnerTrigger, Name: a.Principal.Capability.UnitID, RunID: a.Target.RunID,
			Holder: b.Holder, Generation: b.Generation,
		}
	}
	return actor
}

func principalCapabilityPrefix(p capability.Principal) string {
	if p.Authority.Kind == capability.AuthorityCapability {
		return p.Capability.Prefix
	}
	return ""
}

func invalidEngineBody(message string) *apierr.Error {
	return &apierr.Error{Code: apierr.BadRequest, Message: message}
}

var _ RunTransactions = (*store.Tenant)(nil)
