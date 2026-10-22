package store

import (
	"context"
	"fmt"
	"time"
)

// SingletonLease is fenced so a former replica cannot continue maintenance after another replica takes over.
type SingletonLease struct {
	Name       string
	Holder     string
	Generation int64
	ExpiresAt  time.Time
}

func validSingletonWork(name string) bool {
	switch name {
	case "controller.reaper", "controller.owner", "completion", "maintenance", "schedules.tick":
		return true
	}
	return false
}

// AcquireSingletonLease refuses a live foreign holder and advances the fence after a lapse.
func (o *Operator) AcquireSingletonLease(ctx context.Context, name, holder string, ttl time.Duration) (SingletonLease, error) {
	if !validSingletonWork(name) || holder == "" || ttl <= 0 {
		return SingletonLease{}, fmt.Errorf("invalid singleton lease")
	}
	return inTxResult(ctx, o.s, func(tx *storeTx) (SingletonLease, error) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO leases(name,holder,until,generation) VALUES (?,'',0,0) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return SingletonLease{}, err
		}
		var prior string
		var until, generation int64
		if err := tx.QueryRowContext(ctx, `SELECT holder,until,generation FROM leases WHERE name = ?`+tx.forUpdate(), name).Scan(&prior, &until, &generation); err != nil {
			return SingletonLease{}, err
		}
		now := tx.now(ctx)
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
		return SingletonLease{Name: name, Holder: holder, Generation: generation, ExpiresAt: expires}, nil
	})
}

// RenewSingletonLease refuses an expired fence; owner recovery acquires a new lease instead of reviving one.
func (o *Operator) RenewSingletonLease(ctx context.Context, lease SingletonLease, ttl time.Duration) (SingletonLease, error) {
	if ttl <= 0 || !validSingletonWork(lease.Name) {
		return SingletonLease{}, fmt.Errorf("invalid singleton renewal")
	}
	return inTxResult(ctx, o.s, func(tx *storeTx) (SingletonLease, error) {
		if _, err := tx.ExecContext(ctx, `SELECT holder FROM leases WHERE name=?`+tx.forUpdate(), lease.Name); err != nil {
			return SingletonLease{}, err
		}

		if err := assertSingletonLeaseTx(ctx, tx, lease, lease.Name); err != nil {
			return SingletonLease{}, err
		}
		lease.ExpiresAt = tx.now(ctx).Add(ttl)
		if _, err := tx.ExecContext(ctx, `UPDATE leases SET until = ? WHERE name = ?`, lease.ExpiresAt.UnixNano(), lease.Name); err != nil {
			return SingletonLease{}, err
		}
		return lease, nil
	})
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

// FinancialTables identifies records that never take a cascading run foreign key.
func FinancialTables() []string {
	return []string{
		"credit_grants", "credit_charges", "credit_checkouts", "credit_freezes",
		"billing_awards", "billable_intervals",
		"card_refunds", "payment_warnings", "payment_cards", "payment_refunds", "payment_setups", "payment_states",
		"storage_run_usage", "storage_month_usage",
	}
}

// RetentionRequest is executed only while the maintenance lease is live; settings are resolved inside the store.
type RetentionRequest struct {
	Lease SingletonLease
	Limit int
}

// RetentionResult reports committed removals and preserved pins; financial rows must always remain untouched.
type RetentionResult struct {
	AuditEvents   int64
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
	return t.s.inTx(ctx, func(tx *storeTx) error {
		result, err := tx.ExecContext(ctx, query, t.team, delta.Period, delta.ReplicaID, delta.Amount, tx.now(ctx).UnixNano())
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
	})
}

// ReserveQuota locks one shared team/quota row; log reads are exempt from download quota.
func (t *Tenant) ReserveQuota(ctx context.Context, quota string, amount int64) (QuotaReservation, error) {
	return t.reserveQuota(ctx, quota, amount)
}

// SettleQuota commits actual usage or releases the reservation in one transaction.
func (t *Tenant) SettleQuota(ctx context.Context, reservationID string, used int64) error {
	return t.settleQuota(ctx, reservationID, used)
}
