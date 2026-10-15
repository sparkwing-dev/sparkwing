package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// UnitRef selects named work; nil UnitDemand.Target selects the ordered eligible queue.
// Eval includes its immutable source generation. Trigger and plan need only RunID.
type UnitRef struct {
	Kind             capability.UnitKind
	RunID            string
	NodeID           string
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
func (s *Store) ClaimUnit(ctx context.Context, demand UnitDemand) (UnitClaim, error) {
	return s.claimUnit(ctx, demand)
}

// ReleaseUnit returns an unstarted unit without consuming an attempt; cancellation and started leases use transitions.
func (s *Store) ReleaseUnit(ctx context.Context, claim UnitClaim, capabilityPrefix string) error {
	return s.releaseUnit(ctx, claim, capabilityPrefix)
}

// OperatorStore restricts deployment work to explicit methods without embedding a tenant or raw Store.
type OperatorStore interface {
	AcquireSingletonLease(context.Context, string, string, time.Duration) (SingletonLease, error)
	RenewSingletonLease(context.Context, SingletonLease, time.Duration) (SingletonLease, error)
	DueRunEffects(context.Context, SingletonLease, int) ([]DesiredRunEffect, error)
	MaintainRetention(context.Context, RetentionRequest) (RetentionResult, error)
}

var _ OperatorStore = (*Operator)(nil)

// safety: named, queued and self claims derive authority from the same stored rows.
// Profile reservations and identity come from the live capability; Demand comes
// from the run seal and the selected unit's persisted execution settings.
func matchingInputsTx(ctx context.Context, tx *storeTx, request UnitDemand, unit UnitRef) (match.Profile, match.Demand, error) {
	return resolveMatchingInputsTx(ctx, tx, request, unit)
}

// ClaimProfileResolver supplies enrolled placement facts and current host capacity.
// Implementations must bind every result to the supplied authenticated principal.
// ResolveClaimProfile runs inside a store transaction and must not mutate store state.
type ClaimProfileResolver interface {
	ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error)
}

// SetClaimProfileResolver installs enrollment and self-claim observations before concurrent use.
func (s *Store) SetClaimProfileResolver(resolver ClaimProfileResolver) { s.claimProfiles = resolver }
