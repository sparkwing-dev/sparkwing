package store

import (
	"context"
	"time"
)

// SingletonLease is fenced so a former replica cannot continue maintenance after another replica takes over.
type SingletonLease struct {
	Name       string
	Holder     string
	Generation int64
	ExpiresAt  time.Time
}

// SingletonWork lists every correctness-critical elected worker, including the local schedule worker.
func SingletonWork() []string {
	return []string{"controller.reaper", "controller.owner", "completion", "maintenance", "schedules.tick"}
}

// AcquireSingletonLease refuses a live foreign holder and advances the fence after a lapse.
func (o *Operator) AcquireSingletonLease(ctx context.Context, name, holder string, ttl time.Duration) (SingletonLease, error) {
	panic("unimplemented: c1-data")
}

// RenewSingletonLease refuses an expired fence; owner recovery acquires a new lease instead of reviving one.
func (o *Operator) RenewSingletonLease(ctx context.Context, lease SingletonLease, ttl time.Duration) (SingletonLease, error) {
	panic("unimplemented: c1-data")
}

// ReapedUnit reports recovery in the same run; application retry budgets are never consumed by a lapse.
type ReapedUnit struct {
	Team        Team
	RunID       string
	NodeID      string
	Kind        string
	Generation  int64
	Disruptions int
	Requeued    bool
	Cancelled   bool
	Failed      bool
}

// ReapUnits uses the store clock and cancellation precedence, settling billing and effects with each transition.
func (s *Store) ReapUnits(ctx context.Context, lease SingletonLease, limit int) ([]ReapedUnit, error) {
	panic("unimplemented: c1-data")
}

// RetentionRule separates operational expiration from pins and financial retention.
type RetentionRule struct {
	Data           string
	Setting        string
	CloudDays      *int
	SelfHostedDays *int
	Exception      string
	Accounting     string
}

// RetentionRules is the full leased-pass policy; nil means operator-defined or disabled, never an implicit zero default.
func RetentionRules() []RetentionRule {
	days := func(n int) *int { return &n }
	return []RetentionRule{
		{"runs and operational children", "run_retention_days", days(90), nil, "keep newest successful run of each pipeline", "release usage in deletion transaction"},
		{"events", "run_retention_days", days(90), nil, "expire through retained run deletion", "release event usage in deletion transaction"},
		{"node metrics", "node_metric_retention_days", nil, nil, "independent metric retention setting", "release measured storage"},
		{"outputs", "output_retention", days(30), days(30), "pin newest successful output per pipeline despite run/blob windows", "unique blob accounting"},
		{"funded logs", "log_class_r90", days(90), days(90), "standing changes affect new runs only; operator classes allowed", "object lifecycle or filesystem expiration"},
		{"free logs", "log_class_r30", days(30), days(30), "standing changes affect new runs only; operator classes allowed", "object lifecycle or filesystem expiration"},
		{"financial ledger", "accounting_period", nil, nil, "never cascade or prune by run retention", "preserve grants minus charges"},
		{"blob records", "record_expires_at", nil, nil, "pins survive expiry", "collect unreachable blobs after 24h through tombstones"},
		{"blob receipts", "run_retention_days", days(90), nil, "expire with uploader run", "receipt is not a persistent read edge"},
		{"capabilities", "capability_retention", days(7), days(7), "after expiry or revocation; descendants cannot outlive parent", "preserve independent billing evidence"},
		{"run effects", "effect_retention", days(7), days(7), "terminal run and desired_rev=delivered_rev; age since delivery", "retain undelivered revisions"},
		{"sessions and flow nonces", "expires_at", nil, nil, "existing configured expiry", "no financial deletion"},
		{"webhook deliveries", "delivery_retention", nil, nil, "existing replay retention", "preserve accepted run trust facts"},
	}
}

// FinancialTables identifies records that never take a cascading run foreign key.
func FinancialTables() []string {
	return []string{
		"credit_grants", "credit_charges", "credit_checkouts", "credit_freezes",
		"billing_awards", "billable_intervals",
		"card_charges", "card_attempts", "card_refunds", "payment_warnings",
		"team_spend_days", "card_spend_days", "storage_run_usage", "storage_month_usage",
	}
}

// RetentionRequest is executed only while the maintenance lease is live; settings are resolved inside the store.
type RetentionRequest struct {
	Lease SingletonLease
	Limit int
}

// RetentionResult reports committed removals and preserved pins; financial rows must always remain untouched.
type RetentionResult struct {
	Runs          int64
	Metrics       int64
	Records       int64
	Capabilities  int64
	Effects       int64
	ReleasedBytes int64
	PinnedRuns    int64
	PinnedOutputs int64
}

// MaintainRetention is the single deployment pass for run, metric, object and credential expiration.
func (o *Operator) MaintainRetention(ctx context.Context, request RetentionRequest) (RetentionResult, error) {
	panic("unimplemented: c1-data")
}

// UsageDelta is additive per replica; summed counters are reports and never admission locks.
type UsageDelta struct {
	Period    string
	ReplicaID string
	Counter   string
	Amount    int64
}

// QuotaReservation is backed by the shared team/quota row, preventing two replicas from spending the same capacity.
type QuotaReservation struct {
	ID        string
	Quota     string
	Amount    int64
	ExpiresAt time.Time
}

// AddUsage records accounting under the Tenant with a replica identity, including exact log egress.
func (t *Tenant) AddUsage(ctx context.Context, delta UsageDelta) error {
	panic("unimplemented: c1-data")
}

// ReserveQuota locks one shared team/quota row; log reads are exempt from download quota.
func (t *Tenant) ReserveQuota(ctx context.Context, quota string, amount int64) (QuotaReservation, error) {
	panic("unimplemented: c1-data")
}

// SettleQuota commits actual usage or releases the reservation in one transaction.
func (t *Tenant) SettleQuota(ctx context.Context, reservationID string, used int64) error {
	panic("unimplemented: c1-data")
}
