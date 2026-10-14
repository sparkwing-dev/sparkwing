package store

import (
	"context"
	"time"
)

// UnitKind separates billing and liveness for trigger ownership, planning, evaluation and job execution.
type UnitKind string

// Unit kinds match awarded capabilities and billable interval records.
const (
	UnitTrigger UnitKind = "trigger"
	UnitPlan    UnitKind = "plan"
	UnitEval    UnitKind = "eval"
	UnitNode    UnitKind = "node"
)

// UnitDemand carries self-asserted labels only; granted identity, reservations and team come from C2's capability.
type UnitDemand struct {
	CapabilityPrefix string
	Kinds            []UnitKind
	Labels           []string
	RunnerName       string
	Cores            int64
	MemoryBytes      int64
}

// UnitClaim records the store-derived team and award; source generation remains distinct from eval claim generation.
type UnitClaim struct {
	Team             Team
	Kind             UnitKind
	RunID            string
	NodeID           string
	SourceGeneration int64
	Award            ClaimAward
	Owner            OwnerLeaseRef
	// Bearer is the one-time C2 issuance result; trusted transport must deliver it without storing or logging it.
	Bearer []byte `json:"-"`
}

// ClaimUnit applies the shared matcher and C2 Award atomically; only launcher service scope can cross teams.
func (s *Store) ClaimUnit(ctx context.Context, demand UnitDemand) (UnitClaim, error) {
	panic("unimplemented: c1-data")
}

// ReleaseUnit returns an unstarted unit without consuming an attempt; cancellation and started leases use transitions.
func (s *Store) ReleaseUnit(ctx context.Context, claim UnitClaim, capabilityPrefix string) error {
	panic("unimplemented: c1-data")
}

// OperatorStore restricts deployment work to explicit methods without embedding a tenant or raw Store.
type OperatorStore interface {
	AcquireSingletonLease(context.Context, string, string, time.Duration) (SingletonLease, error)
	RenewSingletonLease(context.Context, SingletonLease, time.Duration) (SingletonLease, error)
	DueRunEffects(context.Context, SingletonLease, int) ([]DesiredRunEffect, error)
	MaintainRetention(context.Context, RetentionRequest) (RetentionResult, error)
}

var _ OperatorStore = (*Operator)(nil)
