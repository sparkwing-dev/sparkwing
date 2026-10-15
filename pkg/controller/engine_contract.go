package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// EngineController adapts the shared HTTP wire to C1's transactions. Middleware
// supplies C2 Authorized values; bodies cannot supply actors, grants or fences.
// Every mutation rechecks that authority transactionally through the store.
type EngineController struct {
	Store         *store.Store
	RunnerPolicy  RunnerAdmissionPolicy
	VersionPolicy *SignedEngineVersionPolicy
}

// ExpansionForStore is the only expansion adapter. It derives Source and Actor
// from the authorized route/binding, compares the requested immutable source
// generation, and requires Body XOR Failure. Both variants become the same
// store.ExpansionSubmission; Failure.Code and masked Message are preserved.
// No acceptance or state mutation happens before Tenant.AcceptExpansion.
func ExpansionForStore(authorized capability.Authorized, body controllerwire.ExpansionSubmission) (store.ExpansionSubmission, error) {
	return expansionForStore(authorized, body, apierr.DecodeInner)
}

func (s *EngineController) AcceptExpansion(ctx context.Context, authorized capability.Authorized, body controllerwire.ExpansionSubmission) (controllerwire.ExpansionAccepted, error) {
	request, err := ExpansionForStore(authorized, body)
	if err != nil {
		return controllerwire.ExpansionAccepted{}, err
	}
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.ExpansionAccepted{}, err
	}
	result, err := tenant.AcceptExpansion(ctx, request)
	if err != nil {
		return controllerwire.ExpansionAccepted{}, err
	}
	return engineExpansion(result)
}

// IntakeForStore keeps tagged session or capability authority separate from the
// claim prefix and preserves body.IdempotencyKey. The store derives its scope.
// Verified App facts come from the authenticated delivery record.
func IntakeForStore(authorized capability.Authorized, body controllerwire.CreateRunRequest) (store.IntakeRequest, error) {
	team, err := engineTeam(authorized)
	if err != nil {
		return store.IntakeRequest{}, err
	}
	if body.Request.Team != "" && body.Request.Team != string(team) {
		return store.IntakeRequest{}, engineBadRequest("request names another team")
	}
	body.Request.Team = string(team)
	request := store.IntakeRequest{Authority: authorized.Principal.Authority, Request: body.Request, IdempotencyKey: body.IdempotencyKey}
	if request.Authority.Kind == capability.AuthorityCapability {
		request.CapabilityPrefix = authorized.Principal.Capability.Prefix
	}
	return request, nil
}

func (s *EngineController) CreateRun(ctx context.Context, authorized capability.Authorized, body controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
	request, err := IntakeForStore(authorized, body)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	result, err := tenant.AdmitRun(ctx, request)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	return engineAdmission(result), nil
}

// AcceptPlan carries body.Declarations unchanged into PlanAcceptance.Seal.Declarations.
// The store validates the author-only layers and resolves against its stored request
// and ceilings; a caller cannot supply replacement intake or operator authority.
func (s *EngineController) AcceptPlan(ctx context.Context, authorized capability.Authorized, body controllerwire.PlanSubmission) (controllerwire.PlanAccepted, error) {
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.PlanAccepted{}, err
	}
	run, err := tenant.ReadRun(ctx, authorized.Target.RunID)
	if err != nil {
		return controllerwire.PlanAccepted{}, err
	}
	request, err := planForStore(authorized, body, run)
	if err != nil {
		return controllerwire.PlanAccepted{}, err
	}
	result, err := tenant.AcceptPlan(ctx, request)
	if err != nil {
		return controllerwire.PlanAccepted{}, err
	}
	return enginePlan(result), nil
}

// ReconcileJob uses C1 ReconcileRequest and returns its persisted node and claim.
// The proposal's source outcome is loaded by the store, never supplied by HTTP.
func (s *EngineController) ReconcileJob(ctx context.Context, authorized capability.Authorized, body controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	actor, err := engineActor(authorized)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	request := store.ReconcileRequest{Ref: store.NodeRef{RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID}, Actor: actor, Policy: store.SourcePolicy(body.Policy), MaterializeOnly: body.MaterializeOnly}
	if body.Source != nil {
		source := reconcileSourceForStore(*body.Source)
		request.Source = &source
	}
	result, err := tenant.ReconcileNode(ctx, request)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	response := controllerwire.ReconcileResponse{Kind: string(result.Kind), Job: engineExecutionJob(result.Node)}
	if result.Source != nil {
		source := engineReuseSource(*result.Source)
		response.Source = &source
	}
	if result.Claim == nil {
		return response, nil
	}
	principal, err := s.Store.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: result.Claim.CapabilityPrefix})
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	run, err := tenant.ReadRun(ctx, request.Ref.RunID)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	response.Claim = engineReconciledClaim(principal.Capability, result, run)
	return response, nil
}

// Claim calls engine.EvaluateRunner against signed policy before a new award, derives total
// capacity from registration and uses reported availability with the shared matcher.
func (s *EngineController) Claim(ctx context.Context, authorized capability.Authorized, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return s.claim(ctx, authorized, body)
}

// SyncLauncher returns only this launcher's held awards, with bearer material omitted.
func (s *EngineController) SyncLauncher(ctx context.Context, authorized capability.Authorized, body controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	panic("unimplemented: controller-wire")
}

// Exchange consumes the authenticated bootstrap once and returns its original unit policy.
func (s *EngineController) Exchange(ctx context.Context, authorized capability.Authorized) (controllerwire.ExchangeResponse, error) {
	return s.exchange(ctx, authorized)
}

// CancelJob requests the named job's cancellation without overwriting a live holder's result.
func (s *EngineController) CancelJob(ctx context.Context, authorized capability.Authorized, body controllerwire.CancelRequest) (controllerwire.Applied, error) {
	actor, err := engineActor(authorized)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	if authorized.Target.RunID == "" || authorized.Target.NodeID == "" {
		return controllerwire.Applied{}, engineBadRequest("cancellation requires a job")
	}
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	result, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: store.NodeRef{RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID}, Actor: actor, To: store.NodeCancelled, Cause: store.CauseCancel, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: body.Reason}}})
	if err != nil {
		return controllerwire.Applied{}, err
	}
	return engineApplied(result), nil
}

// ReadFailure derives the source and generation from authorized.Target and checks its recovery edge.
func (s *EngineController) ReadFailure(ctx context.Context, authorized capability.Authorized) (*nodeproto.JobFailure, error) {
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return nil, err
	}
	source := store.NodeRef{RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID}
	if authorized.Principal.Capability.Unit == capability.UnitNode {
		authorized.Target.NodeID = authorized.Binding.NodeID
	}
	actor, err := engineActor(authorized)
	if err != nil {
		return nil, err
	}
	return tenant.ReadFailure(ctx, source, authorized.Target.SourceGeneration, actor)
}

func (s *EngineController) Renew(ctx context.Context, authorized capability.Authorized, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	if _, err := engineTeam(authorized); err != nil {
		return controllerwire.RenewalResponse{}, err
	}
	if authorized.Principal.Authority.Kind != capability.AuthorityCapability || authorized.Principal.Authority.ID != authorized.Principal.Capability.Prefix {
		return controllerwire.RenewalResponse{}, engineBadRequest("renewal requires an awarded capability")
	}
	beat, err := s.Store.RenewUnit(ctx, authorized.Principal.Capability.Prefix, body.Executing)
	if err != nil {
		return controllerwire.RenewalResponse{}, err
	}
	return controllerwire.RenewalResponse{ExpiresAt: beat.ExpiresAt, CancelRequested: beat.CancelRequested, CancelReason: beat.CancelReason, Watched: beat.Watched, WatchedJobs: beat.WatchedNodes}, nil
}

func (s *EngineController) TransitionJob(ctx context.Context, authorized capability.Authorized, body controllerwire.JobTransitionRequest) (controllerwire.Applied, error) {
	if body.Result != nil && body.Result.Failure != nil {
		if err := validateEngineFailure(body.Result.Failure.Error, apierr.DecodeInner); err != nil {
			return controllerwire.Applied{}, err
		}
	}
	request, err := nodeTransitionForStore(authorized, body)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	result, err := tenant.TransitionNode(ctx, request)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	return engineApplied(result), nil
}

func (s *EngineController) Finish(ctx context.Context, authorized capability.Authorized, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
	if err := validateEngineFailure(body.Error, apierr.DecodeInner); err != nil {
		return controllerwire.Applied{}, err
	}
	request, err := finishForStore(authorized, body)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	var result store.Applied
	if request.Transition.Cause == store.RunPublish {
		result, err = tenant.TransitionRun(ctx, request.Transition)
	} else {
		result, err = tenant.FinishRunAndUnit(ctx, request)
	}
	if err != nil {
		return controllerwire.Applied{}, err
	}
	return engineApplied(result), nil
}

func (s *EngineController) Cancel(ctx context.Context, authorized capability.Authorized, body controllerwire.CancelRequest) (controllerwire.Applied, error) {
	actor, err := engineActor(authorized)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	actor.Kind, actor.Owner = store.ActorOperator, store.OwnerLeaseRef{}
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	result, err := tenant.TransitionRun(ctx, store.RunTransition{RunID: authorized.Target.RunID, To: store.RunCancelled, Cause: store.RunCancel, Actor: actor, Reason: body.Reason})
	if err != nil {
		return controllerwire.Applied{}, err
	}
	return engineApplied(result), nil
}

// Retry carries body.IdempotencyKey through AdmitRun while loading unchanged
// source seals and recording retrier lineage. It never edits the source run.
func (s *EngineController) Retry(ctx context.Context, authorized capability.Authorized, body controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	request := store.IntakeRequest{Authority: authorized.Principal.Authority, IdempotencyKey: body.IdempotencyKey, Request: runspec.Request{Team: string(tenant.Team()), Lineage: runspec.Lineage{RetryOf: authorized.Target.RunID}}}
	if request.Request.Lineage.RetryOf == "" {
		return controllerwire.CreateRunResponse{}, engineBadRequest("retry requires a source run")
	}
	if request.Authority.Kind == capability.AuthorityCapability {
		request.CapabilityPrefix = authorized.Principal.Capability.Prefix
	}
	result, err := tenant.AdmitRun(ctx, request)
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	return engineAdmission(result), nil
}

// RunDetail uses runspec.DisplayRequest/DisplaySpec and the same safe projection
// for local and remote reads. Claim execution inputs are on their private wire.
func (s *EngineController) RunDetail(ctx context.Context, authorized capability.Authorized) (controllerwire.RunDetail, error) {
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.RunDetail{}, err
	}
	view, err := tenant.ReadRunView(ctx, authorized.Target.RunID)
	if err != nil {
		return controllerwire.RunDetail{}, err
	}
	detail := engineRunDetail(view)
	if detail.Engine != nil {
		if s.VersionPolicy == nil {
			return controllerwire.RunDetail{}, versionPolicyUnavailable()
		}
		detail.Notices, err = s.VersionPolicy.Notices(ctx, detail.Engine.Version)
		if err != nil {
			return controllerwire.RunDetail{}, err
		}
	}
	return detail, nil
}

// Snapshot takes one ReadSnapshot and its Scheduling projection, including empty
// generator groups. Remote owners cannot assemble a schedule from separate reads.
func (s *EngineController) Snapshot(ctx context.Context, authorized capability.Authorized) (controllerwire.ScheduleSnapshot, error) {
	tenant, err := s.tenant(ctx, authorized)
	if err != nil {
		return controllerwire.ScheduleSnapshot{}, err
	}
	snapshot, err := tenant.ReadOwnerSnapshot(ctx, authorized.Principal.Authority, authorized.Target.RunID)
	if err != nil {
		return controllerwire.ScheduleSnapshot{}, err
	}
	return engineSchedule(snapshot)
}

// Compatibility serves /api/v1/capabilities?engine_version= from verified release
// metadata. API compatibility and security eligibility remain separate decisions.
func (s *EngineController) Compatibility(ctx context.Context, engineVersion string) (controllerwire.Compatibility, error) {
	if engineVersion == "" {
		return controllerwire.Compatibility{}, engineBadRequest("engine_version is required")
	}
	if s.VersionPolicy == nil {
		return controllerwire.Compatibility{}, versionPolicyUnavailable()
	}
	return s.VersionPolicy.Compatibility(ctx, engineVersion)
}
