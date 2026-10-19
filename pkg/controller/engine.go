package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// EngineController applies authorized operations through store transactions.
type EngineController struct {
	Store             *store.Store
	RunnerPolicy      RunnerAdmissionPolicy
	LocalRunnerPolicy RunnerAdmissionPolicy
	VersionPolicy     *SignedEngineVersionPolicy
	Units             UnitRenewer
	Launcher          LauncherSyncReader
	ResolveTenant     func(context.Context, store.Team) (RunTransactions, error)
	ResolveRunTeam    func(context.Context, string) (store.Team, error)
	ResolveOperator   func(context.Context) (AuthorityRevoker, error)
	ProjectScheduling func(store.RunSnapshot) (store.ScheduleSnapshot, error)
	ReadRunStatus     func(context.Context, store.Team, string) (RunStatusFacts, error)
	// ConfirmAward rechecks a fresh award before it is returned; false releases the unit unstarted.
	ConfirmAward func(context.Context, store.UnitClaim, string) (bool, error)
}

// ReconcileJob uses C1 ReconcileRequest and returns its persisted node and claim.
func (s *EngineController) ReconcileJob(ctx context.Context, authorized capability.Authorized, body controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	actor := engineActor(authorized)
	tenant, err := s.runTransactions(ctx, authorized.Target.Team)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	request := store.ReconcileRequest{Ref: store.NodeRef{RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID}, Actor: actor, Policy: store.SourcePolicy(body.Policy), MaterializeOnly: body.MaterializeOnly, Reserved: body.Reserved}
	if body.Source != nil {
		source := reconcileSourceForStore(*body.Source)
		request.Source = &source
	}
	result, err := tenant.ReconcileNode(ctx, request)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	response := controllerwire.ReconcileResponse{Kind: string(result.Kind), Job: executionJob(result.Node)}
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
	claimant, err := s.Store.ForTeam(ctx, store.Team(principal.Capability.Team))
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	if response.Claim.OwnerMachine, err = claimant.OwnerMachineClaimant(ctx, principal, run.ID); err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	if len(result.Bearer) > 0 && principal.Capability.Delivery == capability.DeliveryDirect && run.Request.Request.Origin != runspec.OriginLocal && len(run.Request.Request.Env.Names) > 0 {
		owner, err := s.Store.ForTeam(ctx, store.Team(principal.Capability.Team))
		if err != nil {
			return controllerwire.ReconcileResponse{}, err
		}
		response.Claim.SubmittedEnv, err = owner.ReadSubmittedEnvironment(ctx, principal.Authority)
		if err != nil {
			return controllerwire.ReconcileResponse{}, err
		}
	}
	return response, nil
}

// Claim checks signed runner policy and registered capacity before awarding work.
func (s *EngineController) Claim(ctx context.Context, authorized capability.Authorized, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return s.claim(ctx, authorized, body)
}

// SyncLauncher returns only this launcher's held awards, with bearer material omitted.
func (s *EngineController) SyncLauncher(ctx context.Context, authorized capability.Authorized, body controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	return s.syncLauncher(ctx, authorized, body)
}

// Exchange consumes the authenticated bootstrap once and returns its original unit policy.
func (s *EngineController) Exchange(ctx context.Context, authorized capability.Authorized) (controllerwire.ExchangeResponse, error) {
	return s.exchange(ctx, authorized)
}

// Compatibility resolves controller API support from verified engine release metadata.
func (s *EngineController) Compatibility(ctx context.Context, engineVersion string) (controllerwire.Compatibility, error) {
	if engineVersion == "" {
		return controllerwire.Compatibility{}, invalidEngineBody("engine_version is required")
	}
	if s.VersionPolicy == nil {
		return controllerwire.Compatibility{}, versionPolicyUnavailable()
	}
	return s.VersionPolicy.Compatibility(ctx, engineVersion)
}
