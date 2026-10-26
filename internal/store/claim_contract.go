package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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

// UnitDemand carries observed platform and labels. Identity, reservations and team
// come from C2's capability; requirements come from persisted seals, and pool access is
// decided at each use from the current pool rules.
type UnitDemand struct {
	// FreeSlots is remaining poll capacity; nil offers one slot. Awards consume it.
	FreeSlots          *int
	CapabilityPrefix   string
	Kinds              []capability.UnitKind
	Labels             []string
	JobNamespace       string
	ExcludedTeamLabels []string
	Target             *UnitRef
	OS                 string
	Arch               string
	// Kubernetes is the runner's report that it runs in a Kubernetes pod.
	Kubernetes bool
}

// UnitClaim records the store-derived team and award; source generation remains distinct from eval claim generation.
type UnitClaim struct {
	Team             Team
	Kind             capability.UnitKind
	RunID            string
	NodeID           string
	SourceGeneration int64
	Award            runstate.ClaimAward
	Owner            runstate.OwnerLeaseRef
	// Bearer is the one-time C2 issuance result; trusted transport must deliver it without storing or logging it.
	Bearer []byte `json:"-"`
}

// ClaimUnit selects one eligible unit and rechecks its requirements before
// C2 Award in the same transaction. Named and queued trigger, plan, eval and job
// claims use the same path; only launcher service scope can cross teams.
// ReconcileNode self-claims recheck the same SQL predicate before their award.
// Each requested kind requires its own live grant before fair-share and arrival ordering. Empty,
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
func (o *Operator) RenewUnit(ctx context.Context, capabilityPrefix string, executing bool) (runstate.LeaseBeat, error) {
	return o.s.renewUnit(ctx, capabilityPrefix, executing)
}

// OperatorStore restricts deployment work to explicit methods without embedding a tenant or raw Store.
type OperatorStore interface {
	AcquireSingletonLease(context.Context, string, string, time.Duration) (SingletonLease, error)
	RenewSingletonLease(context.Context, SingletonLease, time.Duration) (SingletonLease, error)
	ReconcileControllerRuns(context.Context, SingletonLease, string, int) ([]ControllerRun, error)
	DueRunEffects(context.Context, SingletonLease, int) ([]DesiredRunEffect, error)
	MaintainRetention(context.Context, RetentionRequest) (RetentionResult, error)
	MaintainConcurrency(context.Context, SingletonLease) (ConcurrencyMaintenanceResult, error)
}

var _ OperatorStore = (*Operator)(nil)

// safety: named, queued and self claims derive authority from the same stored rows.
// Profile reservations and identity come from the live capability; Demand comes
// from the run seal and the selected unit's persisted execution settings.
func matchingInputsTx(ctx context.Context, tx *storeTx, request UnitDemand, unit UnitRef) (match.Profile, match.Demand, error) {
	return resolveMatchingInputsTx(ctx, tx, request, unit)
}
