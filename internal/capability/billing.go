package capability

import "time"

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

// BillingProvenance is immutable at Award and survives Exchange and claimant revocation.
// PayerTeam comes from the awarded row, Metered from the root or launcher service authority.
type BillingProvenance struct {
	PayerTeam     string       `json:"payer_team"`
	Metered       bool         `json:"metered"`
	ExecutorClass string       `json:"executor_class"`
	ReservationID string       `json:"reservation_id"`
	Start         BillingStart `json:"billing_start"`
	Unit          BillingUnit  `json:"unit"`
}

// BillingRule keeps plan-cache hits and customer-runner waits out of settlement.
type BillingRule struct {
	Unit                      BillingUnit
	MinimumSeconds            int64
	PerSecond                 bool
	ExcludeCustomerRunnerWait bool
	CacheHitFree              bool
}

// BillingRules retains the host minimum while separating plan/eval compute from it.
// A minimum is charged only after billable compute starts; an unstarted award refunds in full.
var BillingRules = []BillingRule{
	{BillingPlan, 0, true, true, true},
	{BillingEval, 0, true, true, false},
	{BillingJob, 60, true, true, false},
	{BillingHost, 60, true, true, false},
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
	panic("unimplemented: c2-credentials")
}
