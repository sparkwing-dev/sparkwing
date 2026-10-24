package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// EngineController applies authorized operations through store transactions.
type EngineController struct {
	authority         engineAuthority
	operator          AuthorityRevoker
	authorization     routepolicy.Authorization
	RunnerPolicy      RunnerAdmissionPolicy
	LocalRunnerPolicy RunnerAdmissionPolicy
	VersionPolicy     *SignedEngineVersionPolicy
	Units             UnitRenewer
	Launcher          LauncherSyncReader
	ResolveTenant     func(context.Context, store.Team) (TenantHandle, error)
	ResolveOperator   func(context.Context) (AuthorityRevoker, error)
	ProjectScheduling func(store.RunSnapshot) (store.ScheduleSnapshot, error)
	ReadRunStatus     func(context.Context, store.Team, string) (RunStatusFacts, error)
}

// ReconcileJob uses C1 ReconcileRequest and returns its persisted node and claim.
func (s *EngineController) ReconcileJob(ctx context.Context, authorized capability.Authorized, body controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	actor := engineActor(authorized)
	tenant, err := s.runTransactions(ctx, authorized.Target.Team)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	request := store.ReconcileRequest{Ref: runspec.NodeRef{RunID: runspec.RunID(authorized.Target.RunID), NodeID: runspec.NodeID(authorized.Target.NodeID)}, Actor: actor, Policy: runstate.SourcePolicy(body.Policy), MaterializeOnly: body.MaterializeOnly}
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
	claim, err := s.claimedUnit(ctx, store.UnitClaim{Team: store.Team(authorized.Target.Team), Kind: capability.UnitNode, RunID: string(request.Ref.RunID), NodeID: string(request.Ref.NodeID), Award: *result.Claim, Bearer: result.Bearer})
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	response.Claim = &claim
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

// compatibility resolves controller API support from verified engine release metadata.
func (s *EngineController) compatibility(ctx context.Context, engineVersion string) (controllerwire.Compatibility, error) {
	if engineVersion == "" {
		return controllerwire.Compatibility{}, invalidEngineBody("engine_version is required")
	}
	if s.VersionPolicy == nil {
		return controllerwire.Compatibility{}, versionPolicyUnavailable()
	}
	return s.VersionPolicy.compatibility(ctx, engineVersion)
}

// Cross-team claims and credential exchange resolve authority before tenant selection.
type engineAuthority interface {
	capability.AuthorityResolver
	capability.AuthenticationReader
	ReadClaimProfile(context.Context, capability.AuthorityRef) (match.Profile, error)
	RecordRunnerPoll(context.Context, store.UnitDemand) error
	ClaimUnit(context.Context, store.UnitDemand) (store.UnitClaim, error)
	CloudAwardResources(context.Context, capability.AuthorityRef) (*match.Resources, error)
	ExchangeCapability(context.Context, capability.ExchangeSpec) (capability.Issued, error)
	DelegateCapability(context.Context, capability.DelegateSpec) (capability.Issued, error)
	ResupplyHereSecrets(context.Context, capability.AuthorityRef, map[string]string) error
}
