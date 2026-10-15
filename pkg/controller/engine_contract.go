package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// EngineController adapts authorized controller operations to C1's transactions. Middleware
// supplies C2 Authorized values; bodies cannot supply actors, grants or fences.
// Every mutation rechecks that authority transactionally through the store.
type EngineController struct {
	Store         *store.Store
	RunnerPolicy  RunnerAdmissionPolicy
	VersionPolicy *SignedEngineVersionPolicy
	Units         UnitRenewer
	Launcher      LauncherSyncReader
	// ResolveTenant supplies the same transaction contract to embedded controllers and tests.
	// A nil resolver uses Store.ForTeam.
	ResolveTenant func(context.Context, store.Team) (RunTransactions, error)
	// ResolveRunTeam supplies resource ownership before authorization. A nil
	// resolver uses Store.AsOperator().RunTeam.
	ResolveRunTeam func(context.Context, string) (store.Team, error)
	// ResolveOperator supplies deployment revocation without exposing a tenant.
	// A nil resolver uses Store.AsOperator.
	ResolveOperator func(context.Context) (AuthorityRevoker, error)
	// ProjectScheduling supplies the shared graph projection to embedded controllers
	// and tests. A nil projector uses store.RunSnapshot.Scheduling.
	ProjectScheduling func(store.RunSnapshot) (store.ScheduleSnapshot, error)
	// ReadRunStatus overrides the store's consistent status read for embedded controllers.
	ReadRunStatus func(context.Context, store.Team, string) (RunStatusFacts, error)
}

// ReconcileJob uses C1 ReconcileRequest and returns its persisted node and claim.
// The proposal's source outcome is loaded by the store, never supplied by HTTP.
func (s *EngineController) ReconcileJob(ctx context.Context, authorized capability.Authorized, body controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	actor := engineActor(authorized)
	tenant, err := s.runTransactions(ctx, authorized.Target.Team)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	request := store.ReconcileRequest{Ref: store.NodeRef{RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID}, Actor: actor, Policy: store.SourcePolicy(body.Policy), MaterializeOnly: body.MaterializeOnly}
	if body.Source != nil {
		request.Source = &store.MaterializeSource{Kind: store.SourcePolicy(body.Source.Policy), Source: body.Source.Attempt, Record: body.Source.Record}
	}
	result, err := tenant.ReconcileNode(ctx, request)
	if err != nil {
		return controllerwire.ReconcileResponse{}, err
	}
	response := controllerwire.ReconcileResponse{Kind: string(result.Kind), Job: executionJob(result.Node)}
	if result.Source != nil {
		response.Source = &controllerwire.ReuseSource{Policy: string(result.Source.Kind), Attempt: result.Source.Source, Record: result.Source.Record}
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
	return s.syncLauncher(ctx, authorized, body)
}

// Exchange consumes the authenticated bootstrap once and returns its original unit policy.
func (s *EngineController) Exchange(ctx context.Context, authorized capability.Authorized) (controllerwire.ExchangeResponse, error) {
	return s.exchange(ctx, authorized)
}

// Compatibility serves /api/v1/capabilities?engine_version= from verified release
// metadata. API compatibility and security eligibility remain separate decisions.
func (s *EngineController) Compatibility(ctx context.Context, engineVersion string) (controllerwire.Compatibility, error) {
	if engineVersion == "" {
		return controllerwire.Compatibility{}, invalidEngineBody("engine_version is required")
	}
	if s.VersionPolicy == nil {
		return controllerwire.Compatibility{}, versionPolicyUnavailable()
	}
	return s.VersionPolicy.Compatibility(ctx, engineVersion)
}
