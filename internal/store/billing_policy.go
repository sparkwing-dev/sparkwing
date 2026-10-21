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
	tier string
	// safety: backingCents is the prepaid balance that backs this tier, at most one tier above the history tier.
	backingCents int64
	vCPU         int64
	duration     time.Duration
	paidCents    int64
	cleanAge     time.Duration
	prepay       bool
	// safety: refillCents, refillsPerDay and refillWeekCents cap refills as a stolen-card brake; zero leaves the
	// team's own limits.
	refillCents, refillsPerDay, refillWeekCents int64
}

// safety: the ratified decision 27 caps; a team that outgrows them asks for the next tier.
var billingPolicy = [...]billingPolicyRow{
	{"New", 0, 8, 4 * time.Hour, 0, 0, true, 5000, 1, 10000},
	{"Established", 2000, 32, 4 * time.Hour, 0, 0, true, 20000, 2, 50000},
	{"Trusted", 10000, 128, 6 * time.Hour, 5000, 30 * 24 * time.Hour, true, 100000, 3, 300000},
	{"High", 50000, 512, 6 * time.Hour, 50000, 30 * 24 * time.Hour, false, 0, 0, 0},
}

// StandingHistory is the payment record a team's history tier is earned from.
type StandingHistory struct {
	Tier           string
	PaidMicro      int64
	FirstPaidAt    time.Time
	LastIncidentAt time.Time
}

type standingPolicy struct {
	row           billingPolicyRow
	history       StandingHistory
	prepayCeiling string
}

func (h StandingHistory) earns(row billingPolicyRow, now time.Time) bool {
	earned := h.PaidMicro > 0
	if row.paidCents > 0 {
		earned = h.PaidMicro >= row.paidCents*MicroCreditsPerCent
	}
	if row.cleanAge > 0 {
		earned = earned && !h.FirstPaidAt.IsZero() && !now.Before(h.FirstPaidAt.Add(row.cleanAge)) && (h.LastIncidentAt.IsZero() || !now.Before(h.LastIncidentAt.Add(row.cleanAge)))
	}
	return earned
}

func prepayCeiling(earned int) int {
	if earned+1 < len(billingPolicy) && billingPolicy[earned+1].prepay {
		return earned + 1
	}
	return earned
}

func standingPolicyTx(ctx context.Context, q rowQuerier, team Team, now time.Time, trust string, held bool) (standingPolicy, error) {
	var paid, first, incident sql.NullInt64
	var p standingPolicy
	err := q.QueryRowContext(ctx, `SELECT
 (SELECT SUM(amount_micro) FROM credit_grants WHERE team=? AND kind IN (?,?)),
 (SELECT MIN(g.created_at) FROM credit_grants g WHERE g.team=? AND g.kind=? AND
 g.amount_micro+COALESCE((SELECT SUM(r.amount_micro) FROM credit_grants r WHERE r.team=g.team AND r.kind='reversal' AND r.reverses=g.reference AND g.reference<>''),0)>0),
 (SELECT MAX(COALESCE(released_at,created_at)) FROM credit_freezes WHERE team=? AND no_fault=0)`, team, CreditGrantPaid, CreditGrantReversal, team, CreditGrantPaid, team).Scan(&paid, &first, &incident)
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
	for i, row := range billingPolicy[1:] {
		if p.history.earns(row, now) {
			earned = i + 1
		}
		if balance >= row.backingCents*MicroCreditsPerCent {
			backed = i + 1
		}
	}
	ceiling := prepayCeiling(earned)
	p.history.Tier, p.prepayCeiling = billingPolicy[earned].tier, billingPolicy[ceiling].tier
	p.row = billingPolicy[max(earned, min(backed, ceiling))]
	switch {
	case held || trust == BillingTrustRevoked:
		p.row = billingPolicyRow{tier: "Held"}
	case trust == BillingTrustGranted:
		p.row = billingPolicy[len(billingPolicy)-1]
	}
	return p, nil
}

// safety: callers hold the team's ledger lock, so two claims cannot both take the last vCPU.
func checkCloudCapacityTx(ctx context.Context, l *ledgerTx, cores int64) error {
	tx, team := l.storeTx, l.team
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return clockErr1
	}
	standing, err := trustStandingTx(ctx, tx, team, clockTime1)
	if err != nil {
		return err
	}
	if standing.Tier == "Held" {
		return &InsufficientCreditsError{Hold: standing.HoldCause}
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
