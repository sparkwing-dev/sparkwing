package capability

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// BillingStart prevents a launch reservation from charging before the pod exists.
type BillingStart string

const (
	BillingAtAward BillingStart = "at_award"
	BillingAtStart BillingStart = "at_start"
)

// BillingUnit separates planning and evaluation from execution minimums.
type BillingUnit string

const (
	BillingPlan BillingUnit = "plan"
	BillingEval BillingUnit = "eval"
	BillingJob  BillingUnit = "job"
	BillingHost BillingUnit = "host"
)

// UnitBilling ties operational persistence to its financial classification.
type UnitBilling struct {
	Operational UnitKind
	Billing     BillingUnit
}

// UnitBillingMappings survives here issuance and bootstrap exchange unchanged.
var UnitBillingMappings = []UnitBilling{
	{UnitTrigger, BillingHost},
	{UnitNode, BillingJob},
	{UnitPlan, BillingPlan},
	{UnitEval, BillingEval},
}

// BillingProvenance snapshots placement-derived billing at Award and survives Exchange and claimant revocation.
type BillingProvenance struct {
	PayerTeam     string       `json:"payer_team"`
	Metered       bool         `json:"metered"`
	ExecutorClass string       `json:"executor_class"`
	ReservationID string       `json:"reservation_id"`
	Start         BillingStart `json:"billing_start"`
	Unit          BillingUnit  `json:"unit"`
}

type billingRule struct {
	Unit                      BillingUnit
	MinimumSeconds            int64
	PerSecond                 bool
	ExcludeCustomerRunnerWait bool
	CacheHitFree              bool
}

// safety: plan and eval compute bill per second apart from the host minimum, and a minimum is charged only after
// billable compute starts, so an unstarted award refunds in full.
var billingRules = []billingRule{
	{BillingPlan, 0, true, true, true},
	{BillingEval, 0, true, true, false},
	{BillingJob, int64(limits.WorkBillingMinimum / time.Second), true, true, false},
	{BillingHost, int64(limits.HostBillingMinimum / time.Second), true, true, false},
}

// BillableInterval records actual compute under one award, excluding customer-owned runner waits.
// Overlapping intervals must be rejected or coalesced by settlement, never double charged.
type BillableInterval struct {
	CapabilityPrefix string
	Start            time.Time
	End              time.Time
	Unit             BillingUnit
}

// BillingSettlement is calculated from immutable provenance and persisted intervals, not a live token.
type BillingSettlement struct {
	ReservationID   string
	BillableSeconds int64
	RefundUnstarted bool
}

// SettleBilling applies the unit minimum once per reservation after excluding nonbillable time.
func SettleBilling(provenance BillingProvenance, intervals []BillableInterval) (BillingSettlement, error) {
	result := BillingSettlement{ReservationID: provenance.ReservationID}
	if provenance.PayerTeam == "" || provenance.ReservationID == "" || provenance.ExecutorClass == "" ||
		(provenance.Start != BillingAtAward && provenance.Start != BillingAtStart) {
		return result, fmt.Errorf("incomplete billing provenance")
	}
	var rule *billingRule
	for i := range billingRules {
		if billingRules[i].Unit == provenance.Unit {
			rule = &billingRules[i]
			break
		}
	}
	if rule == nil {
		return result, fmt.Errorf("unknown billing unit")
	}
	ordered := slices.Clone(intervals)
	slices.SortFunc(ordered, func(a, b BillableInterval) int { return a.Start.Compare(b.Start) })
	var total time.Duration
	var previous time.Time
	for _, interval := range ordered {
		if interval.CapabilityPrefix == "" || interval.Unit != provenance.Unit || interval.Start.IsZero() || interval.End.IsZero() || interval.End.Before(interval.Start) {
			return result, fmt.Errorf("invalid billing interval")
		}
		if interval.Start.Before(previous) {
			return result, fmt.Errorf("overlapping billing intervals")
		}
		elapsed := interval.End.Sub(interval.Start)
		if !interval.Start.Add(elapsed).Equal(interval.End) || elapsed > time.Duration(math.MaxInt64)-total {
			return result, fmt.Errorf("billing interval overflow")
		}
		total += elapsed
		previous = interval.End
	}
	result.RefundUnstarted = len(ordered) == 0
	if !result.RefundUnstarted && provenance.Metered {
		result.BillableSeconds = int64(total / time.Second)
		if total%time.Second != 0 {
			result.BillableSeconds++
		}
		result.BillableSeconds = max(result.BillableSeconds, rule.MinimumSeconds)
	}
	return result, nil
}
