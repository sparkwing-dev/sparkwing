package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// RunTransactions is the controller's tenant transaction and read boundary. Persistence owns validation,
// clocks, replay, and transactional authority rechecks on each mutation.
type RunTransactions interface {
	AuthorityRevoker
	UpdateMembership(context.Context, runstate.Actor, store.MembershipUpdate) error
	Members(context.Context) ([]store.Membership, error)
	AdmitRun(context.Context, store.IntakeRequest) (store.AdmissionResult, error)
	ReplayRun(context.Context, store.IntakeRequest) (store.AdmissionResult, bool, error)
	ReadRun(context.Context, string) (store.RunRow, error)
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	ReadOwnerSnapshot(context.Context, capability.AuthorityRef, string) (store.RunSnapshot, error)
	ReadNode(context.Context, runspec.NodeRef) (runstate.NodeRow, error)
	ReconcileNode(context.Context, store.ReconcileRequest) (runstate.ReconciledNode, error)
	ReadFailure(context.Context, runspec.NodeRef, int64, runstate.Actor) (*nodeproto.JobFailure, error)
	AcceptPlan(context.Context, store.PlanAcceptance) (store.AcceptedPlan, error)
	AcceptExpansion(context.Context, store.ExpansionSubmission) (store.AcceptedExpansion, error)
	TransitionNode(context.Context, store.NodeCommand) (runstate.Applied, error)
	TransitionRun(context.Context, runstate.RunTransition) (runstate.Applied, error)
	FinishRunAndUnit(context.Context, runstate.FinishRunRequest) (runstate.Applied, error)
	WriteAnnotation(context.Context, store.AnnotationWrite) error
	WriteSummary(context.Context, store.SummaryWrite) error
	RecordStep(context.Context, store.StepReport) error
}

func (s *EngineController) runTransactions(ctx context.Context, team string) (RunTransactions, error) {
	return scopedTenantPort[RunTransactions](ctx, store.Team(team), s.ResolveTenant)
}

// IntakeForStore preserves tagged authority and the idempotency key. API input
// cannot assert an actor or verified App facts. Webhook intake supplies its
// authenticated delivery ID through the separate store boundary.
func IntakeForStore(a capability.Authorized, body controllerwire.CreateRunRequest) (store.IntakeRequest, error) {
	p := a.Principal
	if p.Authority.ID == "" || a.Target.Team == "" || p.Capability.Team != a.Target.Team {
		return store.IntakeRequest{}, routepolicy.MissingEndpointPolicy()
	}
	if body.Request.Team != "" && body.Request.Team != a.Target.Team {
		return store.IntakeRequest{}, apierr.Authorization(apierr.ReasonTeam, "intake team does not match authority")
	}
	request := body.Request
	request.Team, request.Dispatch = a.Target.Team, ""
	request.Actor = runspec.Actor{Kind: string(p.Capability.Subject.Kind), ID: p.Capability.Subject.ID}
	// safety: only the verified webhook intake may attach App provenance.
	request.Git.App = runspec.Optional[runspec.AppFacts]{}
	request.Lineage.Retrier = runspec.Optional[runspec.Actor]{}
	if request.Lineage.RetryOf != "" {
		request.Lineage.Retrier = runspec.Optional[runspec.Actor]{Set: true, Value: request.Actor}
	}
	return store.IntakeRequest{Authority: p.Authority, CapabilityPrefix: principalCapabilityPrefix(p), Request: request, IdempotencyKey: body.IdempotencyKey, LocalSecrets: body.LocalSecrets, SubmittedEnv: body.SubmittedEnv, OriginMachine: body.OriginMachine}, nil
}

func (s *EngineController) admitRun(ctx context.Context, team string, request store.IntakeRequest) (controllerwire.CreateRunResponse, error) {
	tenant, err := s.runTransactions(ctx, team)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	admitted, err := tenant.AdmitRun(ctx, request)
	if err != nil {
		return controllerwire.CreateRunResponse{}, engineLookupError(err)
	}
	return createRunResponse(admitted), nil
}

func (s *EngineController) replayRun(ctx context.Context, team string, request store.IntakeRequest) (controllerwire.CreateRunResponse, bool, error) {
	tenant, err := s.runTransactions(ctx, team)
	if err != nil {
		return controllerwire.CreateRunResponse{}, false, err
	}
	admitted, found, err := tenant.ReplayRun(ctx, request)
	if err != nil {
		return controllerwire.CreateRunResponse{}, false, engineLookupError(err)
	}
	return createRunResponse(admitted), found, nil
}

func createRunResponse(admitted store.AdmissionResult) controllerwire.CreateRunResponse {
	return controllerwire.CreateRunResponse{
		RunID: admitted.RunID, Number: admitted.Number, Inputs: admitted.Inputs,
		RequestID: admitted.Request.Request.RequestID, WriteScope: blob.ScopeFor(admitted.Request.Provenance).CapabilityScope(),
		Here:       admitted.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere,
		HereBearer: string(admitted.HereBearer), Replayed: admitted.Replayed,
	}
}

// ExpansionForStore derives the source and actor from authorized resource facts,
// compares its immutable generation, and requires Body XOR Failure. Failure code
// and masked message enter the same transaction as a successful expansion.
func ExpansionForStore(a capability.Authorized, body controllerwire.ExpansionSubmission) (store.ExpansionSubmission, error) {
	if a.Target.RunID == "" || a.Target.NodeID == "" || a.Target.UnitSourceGeneration <= 0 || a.Principal.Authority.ID == "" {
		return store.ExpansionSubmission{}, routepolicy.MissingEndpointPolicy()
	}
	if a.Binding.RunID != a.Target.RunID || a.Binding.NodeID != a.Target.NodeID {
		return store.ExpansionSubmission{}, routepolicy.MissingEndpointPolicy()
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
		Source: runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}, SourceGeneration: runspec.ClaimGeneration(body.SourceGeneration), Actor: actor,
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
		request.Registration = body.Registration
	}
	return request, nil
}

// AcceptExpansion submits success or structured failure through C1's single
// expansion transaction and returns only its persisted topology and replay result.
func (s *EngineController) AcceptExpansion(ctx context.Context, authorized capability.Authorized, body controllerwire.ExpansionSubmission) (controllerwire.ExpansionAccepted, error) {
	if authorized.Principal.Capability.Unit == capability.UnitTrigger && authorized.Binding.Kind == capability.BindingTrigger {
		var err error
		authorized, err = s.sourceExpansionAuthorization(ctx, authorized, body.SourceGeneration)
		if err != nil {
			return controllerwire.ExpansionAccepted{}, err
		}
	}
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

func expansionActor(a capability.Authorized) (runstate.Actor, error) {
	lease := a.Binding.Expansion
	if lease == nil || lease.ID == "" || lease.Holder == "" || lease.Generation <= 0 {
		return runstate.Actor{}, routepolicy.MissingEndpointPolicy()
	}
	kind := runstate.OwnerTrigger
	switch lease.Kind {
	case capability.UnitTrigger:
	case capability.UnitEval:
		kind = runstate.OwnerEval
	default:
		return runstate.Actor{}, routepolicy.MissingEndpointPolicy()
	}
	return runstate.Actor{
		Authority: a.Principal.Authority, Kind: runstate.ActorRunHost, CapabilityPrefix: a.Principal.Capability.Prefix,

		Owner: runstate.OwnerLeaseRef{
			Kind: kind, Name: lease.ID, EvalFence: runspec.EvalFence{RunID: runspec.RunID(a.Target.RunID), SourceNodeID: runspec.NodeID(a.Target.NodeID), SourceGeneration: runspec.ClaimGeneration(a.Target.UnitSourceGeneration), Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)},
		}, NodeFence: runspec.NodeFence{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID), Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)},
	}, nil
}

func expansionResponse(accepted store.AcceptedExpansion) (controllerwire.ExpansionAccepted, error) {
	response := controllerwire.ExpansionAccepted{
		SourceGeneration: int64(accepted.SourceGeneration), State: string(accepted.State), BodyHash: accepted.BodyHash,
		Jobs: make([]controllerwire.ExpandedJob, 0, len(accepted.Nodes)), Replayed: accepted.Replayed,
	}
	for _, node := range accepted.Nodes {
		var job nodeproto.JobSpec
		if err := json.Unmarshal(node.Spec, &job); err != nil || job.ID == "" || node.SpecHash == "" {
			return controllerwire.ExpansionAccepted{}, errors.New("invalid persisted expansion job")
		}
		response.Jobs = append(response.Jobs, controllerwire.ExpandedJob{Job: job, SpecHash: node.SpecHash, Execution: node.Execution, Current: executionJob(node)})
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
		return controllerwire.PlanAccepted{}, routepolicy.MissingEndpointPolicy()
	}
	if run.Request.Request.Mode == runspec.IntakePublish {
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(body.Plan, &plan); err != nil {
			return controllerwire.PlanAccepted{}, invalidEngineBody("invalid publication plan")
		}
		body.Resolved.Env = run.Request.Request.Env
		body.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: run.Request, Declarations: body.Declarations, Plan: plan, Context: settings.Context{Local: true}})
		if err != nil {
			return controllerwire.PlanAccepted{}, err
		}
	}
	accepted, err := tenant.AcceptPlan(ctx, store.PlanAcceptance{
		RunID: a.Target.RunID, Actor: engineActor(a),
		Seal: runspec.SealInput{Declarations: body.Declarations, Request: run.Request, Resolved: body.Resolved, Plan: body.Plan},
	})
	if err != nil {
		return controllerwire.PlanAccepted{}, engineLookupError(err)
	}
	return controllerwire.PlanAccepted{
		Spec: accepted.Spec, Plan: accepted.Plan, PlanHash: runspec.Hash(accepted.PlanHash), Replayed: accepted.Replayed, Notices: []engineid.Notice{},
	}, nil
}

func engineActor(a capability.Authorized) runstate.Actor {
	b := a.Binding
	actor := runstate.Actor{
		Result:    a.Result,
		Authority: a.Principal.Authority, Kind: runstate.ActorRunHost,
		CapabilityPrefix: principalCapabilityPrefix(a.Principal), NodeFence: runspec.NodeFence{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID), Holder: b.Holder, Generation: runspec.ClaimGeneration(b.Generation)},
	}
	if a.Access.Credential == capability.CredentialPublisher {
		actor.Kind = runstate.ActorOperator
	}
	if a.Principal.Capability.Unit == capability.UnitNode {
		actor.Kind = runstate.ActorHolder
	}
	if a.Principal.Capability.Unit == capability.UnitTrigger || a.Principal.Capability.Unit == capability.UnitPlan {
		actor.Owner = runstate.OwnerLeaseRef{
			Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(a.Target.RunID), Holder: b.Holder, Generation: runspec.ClaimGeneration(b.Generation)},
		}
	}
	if a.Principal.Capability.Unit == capability.UnitEval && b.Expansion != nil && b.Expansion.Kind == capability.UnitEval {
		actor.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerEval, EvalFence: runspec.EvalFence{RunID: runspec.RunID(b.RunID), SourceNodeID: runspec.NodeID(b.NodeID), SourceGeneration: runspec.ClaimGeneration(b.Generation), Holder: b.Expansion.Holder, Generation: runspec.ClaimGeneration(b.Expansion.Generation)}}
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
