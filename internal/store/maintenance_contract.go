package store

import (
	"context"
	"fmt"
	"time"
)

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

// financialTables identifies records that never take a cascading run foreign key.
func financialTables() []string {
	return []string{
		"credit_grants", "credit_charges", "credit_checkouts", "credit_freezes",
		"billing_awards", "billable_intervals",
		"card_refunds", "payment_warnings", "payment_refunds", "payment_setups",
		"storage_run_usage", "storage_month_usage",
	}
}

// RetentionRequest bounds one retention pass; settings are resolved inside the store.
type RetentionRequest struct {
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

// UsageDelta is additive per team and period; counters are reports and never admission locks.
type UsageDelta struct {
	Period  string
	Counter string
	Amount  int64
}

// QuotaReservation is backed by the shared team/quota row, so concurrent requests cannot spend the same capacity.
type QuotaReservation struct {
	ID        string
	Quota     string
	Amount    int64
	ExpiresAt time.Time
}

// AddUsage records egress_bytes, download_bytes, storage_bytes, storage_objects or runs.
// The runs counter is the team aggregate because UsageDelta carries no principal.
func (t *Tenant) AddUsage(ctx context.Context, delta UsageDelta) error {
	if delta.Period == "" || delta.Amount < 0 {
		return fmt.Errorf("usage requires a period and nonnegative amount")
	}
	query, err := usageStatement(delta.Counter)
	if err != nil {
		return err
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		result, err := tx.execContext(ctx, query, t.team, delta.Period, delta.Amount, tx.now(ctx).UnixNano())
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
