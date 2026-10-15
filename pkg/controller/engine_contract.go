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
	Store *store.Store
	// ResolveTenant supplies the same transaction contract to embedded controllers and tests.
	// A nil resolver uses Store.ForTeam.
	ResolveTenant func(context.Context, store.Team) (RunTransactions, error)
	// ResolveRunTeam supplies resource ownership before authorization. A nil
	// resolver uses Store.AsOperator().RunTeam.
	ResolveRunTeam func(context.Context, string) (store.Team, error)
	// ResolveOperator supplies deployment revocation without exposing a tenant.
	// A nil resolver uses Store.AsOperator.
	ResolveOperator func(context.Context) (AuthorityRevoker, error)
}

// ReconcileJob uses C1 ReconcileRequest and returns its persisted node and claim.
// The proposal's source outcome is loaded by the store, never supplied by HTTP.
func (s *EngineController) ReconcileJob(ctx context.Context, authorized capability.Authorized, body controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	panic("unimplemented: controller-wire")
}

// Claim calls engine.EvaluateRunner against signed policy before a new award, derives total
// capacity from registration and uses reported availability with the shared matcher.
func (s *EngineController) Claim(ctx context.Context, authorized capability.Authorized, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	panic("unimplemented: controller-wire")
}

// RunDetail uses runspec.DisplayRequest/DisplaySpec and the same safe projection
// for local and remote reads. Claim execution inputs are on their private wire.
func (s *EngineController) RunDetail(ctx context.Context, authorized capability.Authorized) (controllerwire.RunDetail, error) {
	panic("unimplemented: controller-wire")
}

// Snapshot takes one ReadSnapshot and its Scheduling projection, including empty
// generator groups. Remote owners cannot assemble a schedule from separate reads.
func (s *EngineController) Snapshot(ctx context.Context, authorized capability.Authorized) (controllerwire.ScheduleSnapshot, error) {
	panic("unimplemented: controller-wire")
}

// Compatibility serves /api/v1/capabilities?engine_version= from verified release
// metadata. API compatibility and security eligibility remain separate decisions.
func (s *EngineController) Compatibility(ctx context.Context, engineVersion string) (controllerwire.Compatibility, error) {
	panic("unimplemented: controller-wire")
}
