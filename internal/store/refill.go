package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const (
	CheckoutKindCheckout = "checkout"
	CheckoutKindRefill   = "refill"

	BusinessEventRefillOpened   = "billing.refill_opened"
	BusinessEventRefillFailed   = "billing.refill_failed"
	BusinessEventRefillSettings = "billing.refill_settings"

	// MinRefillCents keeps a refill's card fee reasonable.
	MinRefillCents = 1000
	// MaxCardPaymentCents bounds each Add funds payment and automatic refill, at every standing tier.
	MaxCardPaymentCents = 100_000
)

// Refill limits a refill can be stopped by. A team's own weekly and monthly limits come from its settings; the
// standing limits are the decision 27 stolen-card caps its paid history earns.
const (
	RefillLimitPaymentSize    = "payment_size"
	RefillLimitStandingSize   = "standing_size"
	RefillLimitStandingDaily  = "standing_daily"
	RefillLimitStandingWeekly = "standing_weekly"
	RefillLimitTeamWeekly     = "team_weekly"
	RefillLimitTeamMonthly    = "team_monthly"
)

// safety: Stripe keeps an idempotency key for 24 hours, so a refill still unbound after this is closed and the next
// one gets a new key. Unbound means the checkout service never returned an intent, and an intent is only confirmed
// after the controller binds it, so an unbound refill moved no money. A bound refill keeps its slot until its
// payment outcome is known.
const refillKeyLife = 23 * time.Hour

// safety: these closures moved no money, so they neither count against limits nor stop later refills.
const (
	refillExpired  = "expired"
	refillReplaced = "replaced"
)

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

// RefillCaps are the refill caps the team's paid history earns; zero leaves the maximum per payment and the team's own limits.
type RefillCaps struct {
	AmountCents int64
	PerDay      int64
	WeekCents   int64
}

// RefillState is a team's refill settings with what the current windows allow.
type RefillState struct {
	RefillSettings
	Caps RefillCaps
	// FailedAt and Failure name the last refill that failed or was refused; refills wait until the card changes or an
	// Add funds payment succeeds.
	FailedAt time.Time
	Failure  string
	// Open reports a refill in flight.
	Open bool
	// DayCount, WeekCents and MonthCents count in-flight reservations from opening and resolved payments from
	// their outcome, whether granted, refused, refunded or declined. Delayed outcomes start fresh limit windows.
	DayCount   int64
	WeekCents  int64
	MonthCents int64
	// Limit names the limit that stops the next refill, or is empty.
	Limit string
}

// Refill is one refill the worker must create, or reconcile once PaymentIntent is set.
// Its amount, customer, card and receipt address are fixed when it opens, so every retry under its idempotency key
// sends the same request.
type Refill struct {
	Team          Team
	ID            string
	AmountCents   int64
	Customer      string
	PaymentMethod string
	ReceiptEmail  string
	PaymentIntent string
}

func refillStateTx(ctx context.Context, q rowQuerier, team Team, standing BillingStanding, now time.Time) (RefillState, error) {
	var s RefillState
	var enabled int64
	if err := q.QueryRowContext(ctx, `SELECT refill_enabled, refill_threshold_cents, refill_amount_cents,
	    refill_week_limit_cents, refill_month_limit_cents FROM teams WHERE name = ?`,
		string(team)).Scan(&enabled, &s.ThresholdCents, &s.AmountCents, &s.WeekLimitCents, &s.MonthLimitCents); err != nil {
		return s, err
	}
	s.Enabled = enabled == 1
	s.Caps = refillCaps(standing, now)
	var failedAt int64
	err := q.QueryRowContext(ctx, `SELECT closed_at, failure FROM credit_checkouts
	    WHERE team = ? AND refill_blocked = 1 ORDER BY closed_at DESC LIMIT 1`, string(team)).Scan(&failedAt, &s.Failure)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if failedAt > 0 {
		s.FailedAt = time.Unix(0, failedAt)
	}
	window := func(since time.Duration) string {
		return fmt.Sprint(now.Add(-since).UnixNano())
	}
	var open int64
	if err := q.QueryRowContext(ctx, `SELECT
	    COALESCE(SUM(CASE WHEN closed_at IS NULL THEN 1 ELSE 0 END), 0),
	    COALESCE(SUM(CASE WHEN COALESCE(closed_at, opened_at) >= `+window(refillDay)+` THEN 1 ELSE 0 END), 0),
	    COALESCE(SUM(CASE WHEN COALESCE(closed_at, opened_at) >= `+window(refillWeek)+` THEN amount_minor ELSE 0 END), 0),
	    COALESCE(SUM(CASE WHEN COALESCE(closed_at, opened_at) >= `+window(refillMonth)+` THEN amount_minor ELSE 0 END), 0)
	  FROM credit_checkouts WHERE team = ? AND kind = ? AND (COALESCE(closed_at, opened_at) >= ? OR closed_at IS NULL)
	    AND NOT ((payment_id = '' OR failure = 'canceled') AND closed_at IS NOT NULL)`,
		string(team), CheckoutKindRefill, now.Add(-refillMonth).UnixNano()).Scan(&open, &s.DayCount, &s.WeekCents, &s.MonthCents); err != nil {
		return s, err
	}
	s.Open = open > 0
	s.Limit = s.limit()
	return s, nil
}

// safety: refill caps brake a card that has not proven itself, so they follow paid history and its clean age only;
// a balance the card bought, or the capacity tier that balance backs, never loosens them.
func refillCaps(b BillingStanding, now time.Time) RefillCaps {
	row := billingPolicy[0]
	if b.Trust == BillingTrustGranted {
		row = billingPolicy[len(billingPolicy)-1]
	} else {
		for _, candidate := range billingPolicy[1:] {
			if b.History.earns(candidate, now) {
				row = candidate
			}
		}
	}
	return RefillCaps{AmountCents: row.refillCents, PerDay: row.refillsPerDay, WeekCents: row.refillWeekCents}
}

// safety: a refill that would pass any limit is not made at all, so a card is never charged past what its team
// allowed or its standing caps; the team's credits then run down and Cloud work stops at zero.
func (s RefillState) limit() string {
	next := s.AmountCents
	switch {
	case next > MaxCardPaymentCents:
		return RefillLimitPaymentSize
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
	case s.AmountCents > MaxCardPaymentCents:
		return fmt.Errorf("%w: each payment is at most $%d; contact us through the billing request form or sparkwing billing request-tier for larger payments", ErrInvalidInput, MaxCardPaymentCents/100)
	case (s.Enabled || s.AmountCents != 0) && s.AmountCents < MinRefillCents:
		return fmt.Errorf("%w: a refill is at least $%d", ErrInvalidInput, MinRefillCents/100)
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
	    refill_week_limit_cents = ?, refill_month_limit_cents = ?
	    WHERE name = ? AND (? = 0 OR card_payment_method <> '')`,
		boolInt(s.Enabled), s.ThresholdCents, s.AmountCents, s.WeekLimitCents, s.MonthLimitCents, string(team), boolInt(s.Enabled))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmp.Or(err, ErrNoCard)
	}
	change, err := newCreditID("refill-settings")
	if err != nil {
		return err
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return clockErr1
	}
	return tx.operator.RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventRefillSettings, SubjectID: change, Actor: actor, At: clockTime1,
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
		rows, err := tx.QueryContext(ctx, `SELECT name FROM teams WHERE refill_enabled = 1
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
	err = o.EachTeam(ctx, teams, func(t *Tenant) error {
		var r *Refill
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			var err error
			r, err = dueRefillTx(ctx, tx, t.team)
			return err
		})
		if err == nil && r != nil {
			due = append(due, *r)
		}
		return err
	})
	return due, err
}

func dueRefillTx(ctx context.Context, tx *storeTx, team Team) (*Refill, error) {
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	now, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return nil, clockErr2
	}
	st, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return nil, err
	}
	r := Refill{Team: team}
	var opened int64
	var closed string
	err = tx.QueryRowContext(ctx, `SELECT id, amount_minor, payment_id, opened_at, customer_id, payment_method, receipt_email
	    FROM credit_checkouts WHERE team = ? AND kind = ? AND closed_at IS NULL`+tx.forUpdate(), string(team),
		CheckoutKindRefill).Scan(&r.ID, &r.AmountCents, &r.PaymentIntent, &opened, &r.Customer, &r.PaymentMethod, &r.ReceiptEmail)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case r.PaymentIntent != "":
		return &r, nil
	case r.AmountCents > MaxCardPaymentCents || !st.Refill.Enabled || !st.Refill.FailedAt.IsZero() || st.Billing.HoldCause != "":
		closed = refillReplaced
	case now.Sub(time.Unix(0, opened)) < refillKeyLife && r.PaymentMethod == st.Card.PaymentMethod && r.Customer == st.Card.Customer:
		refusal, err := cardRefusalTx(ctx, tx, team, "", st.Card.Fingerprint)
		if err != nil {
			return nil, err
		}
		if refusal.reason == "" {
			return &r, nil
		}
		if err := forgetTeamCardTx(ctx, tx, team, st.Card.Fingerprint, refusal.reason, now); err != nil {
			return nil, err
		}
		closed = refusal.reason
	default:
		closed = refillExpired
		if now.Sub(time.Unix(0, opened)) < refillKeyLife {
			closed = refillReplaced
		}
	}
	if closed != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE credit_checkouts SET closed_at = ?, failure = ?, refill_blocked = ?
		    WHERE team = ? AND id = ? AND payment_id = ''`, now.UnixNano(), closed,
			boolInt(closed != refillExpired && closed != refillReplaced), string(team), r.ID); err != nil {
			return nil, err
		}
		if st, err = spendStandingTx(ctx, tx, team, now); err != nil {
			return nil, err
		}
	}
	refill := st.Refill
	if !refill.Enabled || !refill.FailedAt.IsZero() || st.Billing.HoldCause != "" || refill.Limit != "" ||
		st.BalanceMicro >= refill.ThresholdCents*MicroCreditsPerCent {
		return nil, nil
	}
	if refused, err := cardRefusalTx(ctx, tx, team, "", st.Card.Fingerprint); err != nil || refused.reason != "" {
		return nil, err
	}
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	r = Refill{Team: team, AmountCents: refill.AmountCents, Customer: st.Card.Customer, PaymentMethod: st.Card.PaymentMethod}
	if r.ID, err = newCreditID("refill"); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT a.email FROM memberships m JOIN accounts a ON a.id = m.account_id
	    WHERE m.team = ? AND m.role = 'owner' AND a.email_verified = 1 ORDER BY a.email LIMIT 1`, string(team)).Scan(&r.ReceiptEmail)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err := l.ExecContext(ctx, `INSERT INTO credit_checkouts (id, team, session_id, payment_mode, currency, amount_minor,
	    amount_micro, opened_at, expires_at, kind, customer_id, payment_method, receipt_email)
	    VALUES (?, ?, ?, ?, 'usd', ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, string(team), r.ID, mode, r.AmountCents, r.AmountCents*MicroCreditsPerCent, now.UnixNano(),
		now.Add(refillKeyLife).UnixNano(), CheckoutKindRefill, r.Customer, r.PaymentMethod, r.ReceiptEmail); err != nil {
		return nil, err
	}
	if err := tx.operator.RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventRefillOpened, SubjectID: r.ID, Actor: "system", At: now,
		Attrs: map[string]any{"amount_cents": r.AmountCents, "balance_micro": st.BalanceMicro, "threshold_cents": refill.ThresholdCents},
	}); err != nil {
		return nil, err
	}
	return &r, nil
}

// RecordRefillIntent binds a provider payment to its refill and reconciles provider facts that arrived before binding.
func (o *Operator) RecordRefillIntent(ctx context.Context, team Team, refill, paymentIntent string) error {
	if paymentIntent == "" {
		return fmt.Errorf("%w: a refill intent names its payment", ErrInvalidInput)
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		mode, err := paymentModeTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := lockPaymentTx(ctx, tx, mode, paymentIntent); err != nil {
			return err
		}
		if _, err := lockLedgerTx(ctx, tx, team); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE credit_checkouts SET payment_id = ? WHERE team = ? AND id = ? AND kind = ?
		    AND payment_mode=? AND (closed_at IS NULL OR payment_id=?) AND payment_id IN ('', ?)`, paymentIntent, string(team), refill, CheckoutKindRefill, mode, paymentIntent, paymentIntent)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return cmp.Or(err, authorityError(apierr.Conflict, "refill is closed or bound to another payment"))
		}
		var encoded []byte
		err = tx.QueryRowContext(ctx, `SELECT outcome_json FROM payment_states WHERE payment_mode=? AND payment_id=?`, mode, paymentIntent).Scan(&encoded)
		if errors.Is(err, sql.ErrNoRows) {
			return tx.operator.reconcilePaymentStateTx(ctx, tx, mode, paymentIntent, paymentAttribution{})
		}
		if err != nil {
			return err
		}
		var event capability.PaymentEvent
		if err := json.Unmarshal(encoded, &event); err != nil {
			return err
		}
		_, err = tx.operator.applyPaymentStateTx(ctx, tx, event, "payment_provider")
		return err
	})
}

// CanConfirmRefill rechecks a bound refill against the team's current authorization and limits.
// A reservation preserves the payment's identity, not permission to charge its card.
func (o *Operator) CanConfirmRefill(ctx context.Context, r Refill) (bool, error) {
	var allowed bool
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		if _, err := lockLedgerTx(ctx, tx, r.Team); err != nil {
			return err
		}
		var opened, amount int64
		var customer, method string
		err := tx.QueryRowContext(ctx, `SELECT opened_at, amount_minor, customer_id, payment_method FROM credit_checkouts
		    WHERE team = ? AND id = ? AND kind = ? AND payment_id = ? AND payment_id <> '' AND closed_at IS NULL`,
			string(r.Team), r.ID, CheckoutKindRefill, r.PaymentIntent).Scan(&opened, &amount, &customer, &method)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		now, clockErr3 := tx.now(ctx)
		if clockErr3 != nil {
			return clockErr3
		}
		st, err := spendStandingTx(ctx, tx, r.Team, now)
		if err != nil {
			return err
		}
		if !st.Refill.Enabled || !st.Refill.FailedAt.IsZero() || st.Billing.HoldCause != "" ||
			customer != st.Card.Customer || method != st.Card.PaymentMethod {
			return nil
		}
		refusal, err := cardRefusalTx(ctx, tx, r.Team, r.PaymentIntent, st.Card.Fingerprint)
		if err != nil || refusal.reason != "" {
			return err
		}
		// The windows already include this reservation; replace its contribution with the charge being authorized.
		refill := st.Refill
		refill.AmountCents = amount
		age := now.Sub(time.Unix(0, opened))
		if age <= refillDay {
			refill.DayCount--
		}
		if age <= refillWeek {
			refill.WeekCents -= amount
		}
		if age <= refillMonth {
			refill.MonthCents -= amount
		}
		allowed = refill.limit() == ""
		return nil
	})
	return allowed, err
}

// RecordRefillCanceled closes a bound reservation after the checkout service reports Stripe canceled its intent.
// The record and payment binding remain available for reconciliation and audit.
func (o *Operator) RecordRefillCanceled(ctx context.Context, r Refill) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		clockTime4, clockErr4 := tx.now(ctx)
		if clockErr4 != nil {
			return clockErr4
		}
		_, err := tx.ExecContext(ctx, `UPDATE credit_checkouts SET closed_at = ?, failure = 'canceled'
		    WHERE team = ? AND id = ? AND kind = ? AND payment_id = ? AND payment_id <> '' AND closed_at IS NULL`,
			clockTime4.UnixNano(), string(r.Team), r.ID, CheckoutKindRefill, r.PaymentIntent)
		return err
	})
}
