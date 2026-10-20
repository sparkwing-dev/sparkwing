package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const (
	CheckoutKindCheckout = "checkout"
	CheckoutKindRefill   = "refill"

	BusinessEventRefillOpened   = "billing.refill_opened"
	BusinessEventRefillFailed   = "billing.refill_failed"
	BusinessEventRefillSettings = "billing.refill_settings"

	// MinRefillCents keeps a refill's card fee reasonable (decision 27).
	MinRefillCents = 1000
	// MaxRefillCents is the largest off-session payment the checkout service makes.
	MaxRefillCents = 500_000
)

// Refill limits a refill can be stopped by. A team's own weekly and monthly limits come from its settings; the
// standing limits are the decision 27 stolen-card caps of its tier.
const (
	RefillLimitStandingSize   = "standing_size"
	RefillLimitStandingDaily  = "standing_daily"
	RefillLimitStandingWeekly = "standing_weekly"
	RefillLimitTeamWeekly     = "team_weekly"
	RefillLimitTeamMonthly    = "team_monthly"
)

// safety: Stripe keeps an idempotency key for 24 hours, so a refill whose payment was never confirmed within this
// is closed and the next one gets a new key; an unconfirmed intent moved no money.
const refillKeyLife = 23 * time.Hour

const (
	refillDay   = 24 * time.Hour
	refillWeek  = 7 * refillDay
	refillMonth = 30 * refillDay
)

// RefillSettings is a team's opt-in automatic refill and its own limits on refills, in cents; a zero limit is none.
type RefillSettings struct {
	Enabled         bool
	ThresholdCents  int64
	AmountCents     int64
	WeekLimitCents  int64
	MonthLimitCents int64
}

// RefillCaps are the standing tier's refill caps; zero leaves only the team's own limits.
type RefillCaps struct {
	AmountCents int64
	PerDay      int64
	WeekCents   int64
}

// RefillState is a team's refill settings with what the current windows allow.
type RefillState struct {
	RefillSettings
	Caps RefillCaps
	// FailedAt and Failure name the last refill the card refused; refills wait until the card or the settings change.
	FailedAt time.Time
	Failure  string
	// Open reports a refill in flight.
	Open bool
	// DayCount, WeekCents and MonthCents are the refills made or in flight in the last day, 7 days and 30 days.
	DayCount   int64
	WeekCents  int64
	MonthCents int64
	// Limit names the limit that stops the next refill, or is empty.
	Limit string
}

// Refill is one refill the worker must ask the checkout service to make, or to confirm once PaymentIntent is set.
type Refill struct {
	Team          Team
	ID            string
	AmountCents   int64
	Customer      string
	PaymentMethod string
	PaymentIntent string
}

func refillStateTx(ctx context.Context, q rowQuerier, team Team, standing BillingStanding, now time.Time) (RefillState, error) {
	var s RefillState
	var enabled, failedAt int64
	if err := q.QueryRowContext(ctx, `SELECT refill_enabled, refill_threshold_cents, refill_amount_cents,
	    refill_week_limit_cents, refill_month_limit_cents, refill_failed_at, refill_failure FROM teams WHERE name = ?`,
		string(team)).Scan(&enabled, &s.ThresholdCents, &s.AmountCents, &s.WeekLimitCents, &s.MonthLimitCents,
		&failedAt, &s.Failure); err != nil {
		return s, err
	}
	s.Enabled = enabled == 1
	if failedAt > 0 {
		s.FailedAt = time.Unix(0, failedAt)
	}
	for _, row := range billingPolicy {
		if row.tier == standing.Tier {
			s.Caps = RefillCaps{AmountCents: row.refillCents, PerDay: row.refillsPerDay, WeekCents: row.refillWeekCents}
		}
	}
	window := func(since time.Duration) string {
		return fmt.Sprint(now.Add(-since).UnixNano())
	}
	var open int64
	if err := q.QueryRowContext(ctx, `SELECT
	    COALESCE(SUM(CASE WHEN closed_at IS NULL THEN 1 ELSE 0 END), 0),
	    COALESCE(SUM(CASE WHEN opened_at >= `+window(refillDay)+` THEN 1 ELSE 0 END), 0),
	    COALESCE(SUM(CASE WHEN opened_at >= `+window(refillWeek)+` THEN amount_minor ELSE 0 END), 0),
	    COALESCE(SUM(amount_minor), 0)
	  FROM credit_checkouts WHERE team = ? AND kind = ? AND opened_at >= ? AND (paid_at IS NOT NULL OR closed_at IS NULL)`,
		string(team), CheckoutKindRefill, now.Add(-refillMonth).UnixNano()).Scan(&open, &s.DayCount, &s.WeekCents, &s.MonthCents); err != nil {
		return s, err
	}
	s.Open = open > 0
	s.Limit = s.limit()
	return s, nil
}

// safety: a refill that would pass any limit is not made at all, so a card is never charged past what its team
// allowed or its standing caps; the team's credits then run down and Cloud work stops at zero.
func (s RefillState) limit() string {
	next := s.AmountCents
	switch {
	case s.Caps.AmountCents > 0 && next > s.Caps.AmountCents:
		return RefillLimitStandingSize
	case s.Caps.PerDay > 0 && s.DayCount+1 > s.Caps.PerDay:
		return RefillLimitStandingDaily
	case s.Caps.WeekCents > 0 && s.WeekCents+next > s.Caps.WeekCents:
		return RefillLimitStandingWeekly
	case s.WeekLimitCents > 0 && s.WeekCents+next > s.WeekLimitCents:
		return RefillLimitTeamWeekly
	case s.MonthLimitCents > 0 && s.MonthCents+next > s.MonthLimitCents:
		return RefillLimitTeamMonthly
	}
	return ""
}

func (s RefillSettings) validate() error {
	switch {
	case s.ThresholdCents < 0 || s.WeekLimitCents < 0 || s.MonthLimitCents < 0:
		return fmt.Errorf("%w: refill amounts and limits are not negative", ErrInvalidInput)
	case s.ThresholdCents > MaxCreditGrantMicro/MicroCreditsPerCent || s.WeekLimitCents > MaxCreditGrantMicro/MicroCreditsPerCent || s.MonthLimitCents > MaxCreditGrantMicro/MicroCreditsPerCent:
		return fmt.Errorf("%w: a refill threshold or limit is at most $%d", ErrInvalidInput, MaxCreditGrantMicro/MicroCreditsPerCent/100)
	case s.Enabled && (s.AmountCents < MinRefillCents || s.AmountCents > MaxRefillCents):
		return fmt.Errorf("%w: a refill is between $%d and $%d", ErrInvalidInput, MinRefillCents/100, MaxRefillCents/100)
	case !s.Enabled && s.AmountCents != 0 && (s.AmountCents < MinRefillCents || s.AmountCents > MaxRefillCents):
		return fmt.Errorf("%w: a refill is between $%d and $%d", ErrInvalidInput, MinRefillCents/100, MaxRefillCents/100)
	}
	return nil
}

// ErrNoCard refuses turning on refills for a team with no saved card.
var ErrNoCard = errors.New("card billing: save a card before turning on automatic refill")

func setRefillTx(ctx context.Context, tx *storeTx, team Team, s RefillSettings, actor string) error {
	if err := s.validate(); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE teams SET refill_enabled = ?, refill_threshold_cents = ?, refill_amount_cents = ?,
	    refill_week_limit_cents = ?, refill_month_limit_cents = ?, refill_failed_at = 0, refill_failure = ''
	    WHERE name = ? AND (? = 0 OR card_payment_method <> '')`,
		boolInt(s.Enabled), s.ThresholdCents, s.AmountCents, s.WeekLimitCents, s.MonthLimitCents, string(team), boolInt(s.Enabled))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmp.Or(err, ErrNoCard)
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventRefillSettings, SubjectID: string(team), Actor: actor, At: tx.now(),
		Attrs: map[string]any{
			"enabled": s.Enabled, "threshold_cents": s.ThresholdCents, "amount_cents": s.AmountCents,
			"week_limit_cents": s.WeekLimitCents, "month_limit_cents": s.MonthLimitCents,
		},
	})
}

// DueRefills opens a refill for every opted-in team whose balance fell below its threshold, within its limits, and
// returns each refill still waiting on the checkout service, opened now or earlier.
func (o *Operator) DueRefills(ctx context.Context) ([]Refill, error) {
	var teams []Team
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		rows, err := tx.QueryContext(ctx, `SELECT name FROM teams WHERE refill_enabled = 1 AND refill_failed_at = 0
		    UNION SELECT team FROM credit_checkouts WHERE kind = ? AND closed_at IS NULL ORDER BY 1`, CheckoutKindRefill)
		if err != nil {
			return err
		}
		for rows.Next() {
			var team Team
			if err := rows.Scan(&team); err != nil {
				return errors.Join(err, rows.Close())
			}
			teams = append(teams, team)
		}
		return errors.Join(rows.Err(), rows.Close())
	})
	if err != nil {
		return nil, err
	}
	var due []Refill
	for _, team := range teams {
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			r, err := dueRefillTx(ctx, tx, team)
			if err == nil && r != nil {
				due = append(due, *r)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return due, nil
}

func dueRefillTx(ctx context.Context, tx *storeTx, team Team) (*Refill, error) {
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	now := tx.now()
	st, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return nil, err
	}
	r := Refill{Team: team, Customer: st.Card.Customer, PaymentMethod: st.Card.PaymentMethod}
	var opened int64
	var customer string
	err = tx.QueryRowContext(ctx, `SELECT id, amount_minor, payment_id, opened_at, customer_id FROM credit_checkouts
	    WHERE team = ? AND kind = ? AND closed_at IS NULL`+tx.forUpdate(), string(team), CheckoutKindRefill).Scan(&r.ID, &r.AmountCents, &r.PaymentIntent, &opened, &customer)
	switch {
	case err == nil && now.Sub(time.Unix(0, opened)) < refillKeyLife && st.Card.PaymentMethod != "" && customer == st.Card.Customer:
		return &r, nil
	case err == nil:
		_, err = tx.ExecContext(ctx, `UPDATE credit_checkouts SET closed_at = ?, failure = 'expired' WHERE team = ? AND id = ?`,
			now.UnixNano(), string(team), r.ID)
		return nil, err
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	refill := st.Refill
	if !refill.Enabled || !refill.FailedAt.IsZero() || st.Billing.HoldCause != "" || refill.Limit != "" ||
		st.BalanceMicro >= refill.ThresholdCents*MicroCreditsPerCent {
		return nil, nil
	}
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if r.ID, err = newCreditID("refill"); err != nil {
		return nil, err
	}
	r.AmountCents, r.PaymentIntent = refill.AmountCents, ""
	if _, err := l.ExecContext(ctx, `INSERT INTO credit_checkouts (id, team, session_id, payment_mode, currency, amount_minor,
	    amount_micro, opened_at, expires_at, kind, customer_id) VALUES (?, ?, ?, ?, 'usd', ?, ?, ?, ?, ?, ?)`,
		r.ID, string(team), r.ID, mode, r.AmountCents, r.AmountCents*MicroCreditsPerCent, now.UnixNano(),
		now.Add(refillKeyLife).UnixNano(), CheckoutKindRefill, r.Customer); err != nil {
		return nil, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventRefillOpened, SubjectID: r.ID, Actor: "system", At: now,
		Attrs: map[string]any{"amount_cents": r.AmountCents, "balance_micro": st.BalanceMicro, "threshold_cents": refill.ThresholdCents},
	}); err != nil {
		return nil, err
	}
	return &r, nil
}

// RecordRefillIntent binds the payment intent the checkout service created for an open refill, before it is
// confirmed, so the signed webhook that settles it always finds its refill.
func (o *Operator) RecordRefillIntent(ctx context.Context, team Team, refill, paymentIntent string) error {
	if paymentIntent == "" {
		return fmt.Errorf("%w: a refill intent names its payment", ErrInvalidInput)
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		res, err := tx.ExecContext(ctx, `UPDATE credit_checkouts SET payment_id = ? WHERE team = ? AND id = ? AND kind = ?
		    AND closed_at IS NULL AND payment_id IN ('', ?)`, paymentIntent, string(team), refill, CheckoutKindRefill, paymentIntent)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return cmp.Or(err, authorityError(apierr.Conflict, "refill is closed or bound to another payment"))
		}
		return nil
	})
}
