package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// LaunchAccountLimit is the number of accounts admitted without operator approval.
const LaunchAccountLimit = 100

type billingPolicyRow struct {
	rungCents int64
	tier      string
	vCPU      int64
	duration  time.Duration
	paidCents int64
	cleanAge  time.Duration
}

var billingPolicy = [...]billingPolicyRow{
	{500, "New", 8, 4 * time.Hour, 0, 0},
	{2000, "Established", 32, 4 * time.Hour, 0, 0},
	{5000, "", 0, 0, 0, 0},
	{10000, "Trusted", 128, 6 * time.Hour, 5000, 30 * 24 * time.Hour},
	{25000, "", 0, 0, 0, 0},
	{50000, "High", 512, 6 * time.Hour, 50000, 30 * 24 * time.Hour},
	{100000, "", 0, 0, 0, 0},
}

// StandingHistory is the payment record a team's history tier is earned from.
type StandingHistory struct {
	Tier              string
	PaidMicro         int64
	SuccessfulCharges int64
	FirstPaidAt       time.Time
	LastIncidentAt    time.Time
}

type standingPolicy struct {
	row           billingPolicyRow
	history       StandingHistory
	prepayCeiling string
}

func (h StandingHistory) earns(row billingPolicyRow, now time.Time) bool {
	earned := h.SuccessfulCharges > 0 || h.PaidMicro > 0
	if row.paidCents > 0 {
		earned = h.PaidMicro >= row.paidCents*MicroCreditsPerCent
	}
	if row.cleanAge > 0 {
		earned = earned && !h.FirstPaidAt.IsZero() && !now.Before(h.FirstPaidAt.Add(row.cleanAge)) && (h.LastIncidentAt.IsZero() || !now.Before(h.LastIncidentAt.Add(row.cleanAge)))
	}
	return earned
}

func nextStandingTier(i int) int {
	for j := i + 1; j < len(billingPolicy); j++ {
		if billingPolicy[j].tier != "" {
			return j
		}
	}
	return i
}

func topStandingTier() int {
	i := len(billingPolicy) - 1
	for billingPolicy[i].tier == "" {
		i--
	}
	return i
}

func standingPolicyTx(ctx context.Context, q rowQuerier, team Team, now time.Time, trust string, held bool) (standingPolicy, error) {
	var paid, first, incident sql.NullInt64
	var p standingPolicy
	err := q.QueryRowContext(ctx, `SELECT
 (SELECT SUM(amount_micro) FROM credit_grants WHERE team=? AND kind IN (?,?)),
 (SELECT MIN(g.created_at) FROM credit_grants g WHERE g.team=? AND g.kind=? AND
 g.amount_micro+COALESCE((SELECT SUM(r.amount_micro) FROM credit_grants r WHERE r.team=g.team AND r.kind='reversal' AND r.reverses=g.reference AND g.reference<>''),0)>0),
 (SELECT COUNT(*) FROM card_charges WHERE team=? AND state=?),
 (SELECT MAX(COALESCE(released_at,created_at)) FROM credit_freezes WHERE team=?)`, team, CreditGrantPaid, CreditGrantReversal, team, CreditGrantPaid, team, CardChargePaid, team).Scan(&paid, &first, &p.history.SuccessfulCharges, &incident)
	if err != nil {
		return p, err
	}
	p.history.PaidMicro = paid.Int64
	if first.Valid {
		p.history.FirstPaidAt = time.Unix(0, first.Int64)
	}
	if incident.Valid {
		p.history.LastIncidentAt = time.Unix(0, incident.Int64)
	}
	balance, err := creditBalanceTx(ctx, q, team)
	if err != nil {
		return p, err
	}
	earned, backed := 0, 0
	for i, row := range billingPolicy {
		if row.tier == "" || i == 0 {
			continue
		}
		if p.history.earns(row, now) {
			earned = i
		}
		if balance >= row.rungCents*MicroCreditsPerCent {
			backed = i
		}
	}
	ceiling := nextStandingTier(earned)
	p.history.Tier, p.prepayCeiling = billingPolicy[earned].tier, billingPolicy[ceiling].tier
	p.row = billingPolicy[max(earned, min(backed, ceiling))]
	switch {
	case held || trust == BillingTrustRevoked:
		p.row = billingPolicyRow{tier: "Held"}
	case trust == BillingTrustGranted:
		p.row = billingPolicy[topStandingTier()]
	}
	return p, nil
}

func admitCloudTx(ctx context.Context, tx *storeTx, team Team, cores int64) error {
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	return checkCloudCapacityTx(ctx, tx, team, cores)
}

func checkCloudCapacityTx(ctx context.Context, tx *storeTx, team Team, cores int64) error {
	standing, err := trustStandingTx(ctx, tx, team, tx.now())
	if err != nil {
		return err
	}
	if standing.Tier == "Held" {
		return &InsufficientCreditsError{Limit: SpendLimitChargeFailed}
	}
	if cores > standing.CloudVCPU {
		return authorityError(apierr.BadRequest, "job exceeds "+standing.Tier+" standing Cloud vCPU limit")
	}
	var active int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(c.cpu_class),0) FROM billing_awards a JOIN credit_charges c ON c.team=a.team AND c.capability_prefix=a.capability_prefix AND c.kind='reservation' WHERE a.team=? AND a.executor_class='cloud' AND a.settled_at IS NULL`, team).Scan(&active)
	if err != nil {
		return err
	}
	if active+cores > standing.CloudVCPU {
		return authorityError(apierr.LockHeld, "Cloud work waits for "+standing.Tier+" standing vCPU capacity")
	}
	return nil
}
