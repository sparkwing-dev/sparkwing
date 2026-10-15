package store

import (
	"context"
	"fmt"
	"slices"
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
	if !slices.Contains(SingletonWork(), name) || holder == "" || ttl <= 0 {
		return SingletonLease{}, fmt.Errorf("invalid singleton lease")
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return SingletonLease{}, err
	}
	defer rollbackOrLog(tx)
	if _, err := tx.ExecContext(ctx, `INSERT INTO leases(name,holder,until,generation) VALUES (?,'',0,0) ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return SingletonLease{}, err
	}
	var prior string
	var until, generation int64
	if err := tx.QueryRowContext(ctx, `SELECT holder,until,generation FROM leases WHERE name = ?`+tx.forUpdate(), name).Scan(&prior, &until, &generation); err != nil {
		return SingletonLease{}, err
	}
	now := tx.now()
	if until > now.UnixNano() && prior != holder {
		return SingletonLease{}, ErrLockHeld
	}
	if until <= now.UnixNano() {
		generation++
	}
	expires := now.Add(ttl)
	if _, err := tx.ExecContext(ctx, `UPDATE leases SET holder = ?, until = ?, generation = ? WHERE name = ?`, holder, expires.UnixNano(), generation, name); err != nil {
		return SingletonLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return SingletonLease{}, err
	}
	return SingletonLease{Name: name, Holder: holder, Generation: generation, ExpiresAt: expires}, nil
}

// RenewSingletonLease refuses an expired fence; owner recovery acquires a new lease instead of reviving one.
func (o *Operator) RenewSingletonLease(ctx context.Context, lease SingletonLease, ttl time.Duration) (SingletonLease, error) {
	if ttl <= 0 || !slices.Contains(SingletonWork(), lease.Name) {
		return SingletonLease{}, fmt.Errorf("invalid singleton renewal")
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return SingletonLease{}, err
	}
	defer rollbackOrLog(tx)
	if err := assertSingletonLeaseTx(ctx, tx, lease, lease.Name, tx.now()); err != nil {
		return SingletonLease{}, err
	}
	lease.ExpiresAt = tx.now().Add(ttl)
	if _, err := tx.ExecContext(ctx, `UPDATE leases SET until = ? WHERE name = ?`, lease.ExpiresAt.UnixNano(), lease.Name); err != nil {
		return SingletonLease{}, err
	}
	return lease, tx.Commit()
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
		{"invitations", "spent_identity_retention", days(30), days(30), "after acceptance, withdrawal or expiry", "preserve invitations counted by daily limits"},
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
	Invitations   int64
	Sessions      int64
	ReleasedBytes int64
	PinnedRuns    int64
	PinnedOutputs int64
}

// MaintainRetention is the single deployment pass for run, metric, object and credential expiration.
func (o *Operator) MaintainRetention(ctx context.Context, request RetentionRequest) (RetentionResult, error) {
	return o.maintainRetention(ctx, request)
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

// AddUsage records egress_bytes, download_bytes, storage_bytes, storage_objects or runs per replica.
// The runs counter is the team aggregate because UsageDelta carries no principal.
func (t *Tenant) AddUsage(ctx context.Context, delta UsageDelta) error {
	if delta.Period == "" || delta.ReplicaID == "" || delta.Amount < 0 {
		return fmt.Errorf("usage requires a period, replica and nonnegative amount")
	}
	query, err := usageStatement(delta.Counter)
	if err != nil {
		return err
	}
	result, err := t.s.exec(ctx, query, t.team, delta.Period, delta.ReplicaID, delta.Amount, t.s.now().UnixNano())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("usage counter overflow")
	}
	return nil
}

// ReserveQuota locks one shared team/quota row; log reads are exempt from download quota.
func (t *Tenant) ReserveQuota(ctx context.Context, quota string, amount int64) (QuotaReservation, error) {
	return t.reserveQuota(ctx, quota, amount)
}

// SettleQuota commits actual usage or releases the reservation in one transaction.
func (t *Tenant) SettleQuota(ctx context.Context, reservationID string, used int64) error {
	return t.settleQuota(ctx, reservationID, used)
}
