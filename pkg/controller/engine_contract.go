package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// EngineController adapts the shared HTTP wire to C1's transactions. Middleware
// supplies C2 Authorized values; bodies cannot supply actors, grants or fences.
// Every mutation rechecks that authority transactionally through the store.
type EngineController struct{ Store *store.Store }

// ExpansionForStore is the only expansion adapter. It derives Source and Actor
// from the authorized route/binding, compares the requested immutable source
// generation, and requires Body XOR Failure. Both variants become the same
// store.ExpansionSubmission; Failure.Code and masked Message are preserved.
// No acceptance or state mutation happens before Tenant.AcceptExpansion.
func ExpansionForStore(authorized capability.Authorized, body controllerwire.ExpansionSubmission) (store.ExpansionSubmission, error) {
	panic("unimplemented: controller-wire")
}

func (s *EngineController) AcceptExpansion(ctx context.Context, authorized capability.Authorized, body controllerwire.ExpansionSubmission) (controllerwire.ExpansionAccepted, error) {
	panic("unimplemented: controller-wire")
}

// IntakeForStore keeps tagged session or capability authority separate from the
// claim prefix and preserves body.IdempotencyKey. The store derives its scope.
// Verified App facts come from the authenticated delivery record.
func IntakeForStore(authorized capability.Authorized, body controllerwire.CreateRunRequest) (store.IntakeRequest, error) {
	panic("unimplemented: controller-wire")
}

func (s *EngineController) CreateRun(ctx context.Context, authorized capability.Authorized, body controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
	panic("unimplemented: controller-wire")
}

// AcceptPlan carries body.Declarations unchanged into PlanAcceptance.Seal.Declarations.
// The store validates the author-only layers and resolves against its stored request
// and ceilings; a caller cannot supply replacement intake or operator authority.
func (s *EngineController) AcceptPlan(ctx context.Context, authorized capability.Authorized, body controllerwire.PlanSubmission) (controllerwire.PlanAccepted, error) {
	panic("unimplemented: controller-wire")
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

func (s *EngineController) Renew(ctx context.Context, authorized capability.Authorized, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	panic("unimplemented: controller-wire")
}

func (s *EngineController) TransitionJob(ctx context.Context, authorized capability.Authorized, body controllerwire.JobTransitionRequest) (controllerwire.Applied, error) {
	panic("unimplemented: controller-wire")
}

func (s *EngineController) Finish(ctx context.Context, authorized capability.Authorized, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
	panic("unimplemented: controller-wire")
}

func (s *EngineController) Cancel(ctx context.Context, authorized capability.Authorized, body controllerwire.CancelRequest) (controllerwire.Applied, error) {
	panic("unimplemented: controller-wire")
}

// Retry carries body.IdempotencyKey through AdmitRun while loading unchanged
// source seals and recording retrier lineage. It never edits the source run.
func (s *EngineController) Retry(ctx context.Context, authorized capability.Authorized, body controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
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
