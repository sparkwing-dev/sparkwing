package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// UnitRef selects named work; nil UnitDemand.Target selects the ordered eligible queue.
// Eval includes its immutable source generation. Trigger and plan need only RunID.
type UnitRef struct {
	Kind             capability.UnitKind
	RunID            runspec.RunID
	NodeID           runspec.NodeID
	SourceGeneration int64
}

// UnitDemand carries observed capacity and labels. Identity, reservations and team
// come from C2's capability; requirements and allowed pools come from persisted seals.
type UnitDemand struct {
	CapabilityPrefix string
	Kinds            []capability.UnitKind
	Labels           []string
	Target           *UnitRef
	OS               string
	Arch             string
	Capacity         match.Resources
	Available        match.Resources
}

// UnitClaim records the store-derived team and award; source generation remains distinct from eval claim generation.
type UnitClaim struct {
	Team             Team
	Kind             capability.UnitKind
	RunID            string
	NodeID           string
	SourceGeneration int64
	Award            ClaimAward
	Owner            OwnerLeaseRef
	// Bearer is the one-time C2 issuance result; trusted transport must deliver it without storing or logging it.
	Bearer []byte `json:"-"`
}

// ClaimUnit resolves matching inputs and calls match.Evaluate and match.Fits before
// C2 Award in the same transaction. Named and queued trigger, plan, eval and job
// claims use the same path; only launcher service scope can cross teams.
// ReconcileNode self-claims use the same matching inputs and reservation checks.
// Each requested kind requires its own live grant before one ordered scan. Empty,
// duplicate, unknown and unauthorized kinds fail instead of being filtered silently.
// A named trigger Target also accepts the matching here Award's trigger.host after
// rechecking its first-claim/reclaim window, reservation and requester authority.
func (o *Operator) ClaimUnit(ctx context.Context, demand UnitDemand) (UnitClaim, error) {
	return o.claimUnit(ctx, demand)
}

// ReleaseUnit returns an unstarted unit without consuming an attempt; cancellation and started leases use transitions.
func (s *Store) ReleaseUnit(ctx context.Context, claim UnitClaim, capabilityPrefix string) error {
	return s.releaseUnit(ctx, claim, capabilityPrefix)
}

// RenewUnit derives the unit's team, identity and fences from its live capability.
// Executing reports current owner compute, pausing billing during customer-runner waits; job starts use TransitionNode.
// Cancelled units renew to drain and return CancelRequested without recording a new start.
func (o *Operator) RenewUnit(ctx context.Context, capabilityPrefix string, executing bool) (LeaseBeat, error) {
	return o.s.renewUnit(ctx, capabilityPrefix, executing)
}

// OperatorStore restricts deployment work to explicit methods without embedding a tenant or raw Store.
type OperatorStore interface {
	AcquireSingletonLease(context.Context, string, string, time.Duration) (SingletonLease, error)
	RenewSingletonLease(context.Context, SingletonLease, time.Duration) (SingletonLease, error)
	ReconcileControllerRuns(context.Context, SingletonLease, string, int) ([]ControllerRun, error)
	DueRunEffects(context.Context, SingletonLease, int) ([]DesiredRunEffect, error)
	MaintainRetention(context.Context, RetentionRequest) (RetentionResult, error)
	MaintainConcurrency(context.Context, SingletonLease, ConcurrencyMaintenanceOptions) (ConcurrencyMaintenanceResult, error)
}

var _ OperatorStore = (*Operator)(nil)

// safety: named, queued and self claims derive authority from the same stored rows.
// Profile reservations and identity come from the live capability; Demand comes
// from the run seal and the selected unit's persisted execution settings.
func matchingInputsTx(ctx context.Context, tx *storeTx, request UnitDemand, unit UnitRef) (match.Profile, match.Demand, error) {
	return resolveMatchingInputsTx(ctx, tx, request, unit)
}
