package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const (
	checkoutKindCheckout = "checkout"
	checkoutKindRefill   = "refill"

	businessEventRefillOpened   = "billing.refill_opened"
	businessEventRefillFailed   = "billing.refill_failed"
	businessEventRefillSettings = "billing.refill_settings"

	// MinCardPaymentCents and MaxCardPaymentCents bound each Add funds payment and automatic refill, at every standing
	// tier. The checkout service holds the same pair, pinned by the shared checkout contract fixture.
	MinCardPaymentCents = 1_000
	MaxCardPaymentCents = 100_000
)

// Refill limits a refill can be stopped by. A team's own weekly and monthly limits come from its settings; the
// standing limits are the decision 27 stolen-card caps its paid history earns.
const (
	refillLimitPaymentSize    = "payment_size"
	refillLimitStandingSize   = "standing_size"
	refillLimitStandingDaily  = "standing_daily"
	refillLimitStandingWeekly = "standing_weekly"
	refillLimitTeamWeekly     = "team_weekly"
	refillLimitTeamMonthly    = "team_monthly"
)

// safety: Stripe keeps an idempotency key for at least 24 hours, so a refill is retried under its key only this long;
// after that it is closed as expired and the next refill gets a new key. A late signed outcome still settles it.
const refillKeyLife = 23 * time.Hour

// A refill closed without an outcome: expired after its key life, or withdrawn when a recheck before a retry found
// the team no longer eligible. Unless a payment binds to it, from the checkout service's answer or a later signed
// outcome, it neither counts against limits nor stops later refills.
const (
	refillExpired   = "expired"
	refillWithdrawn = "withdrawn"
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
	// FailedAt and Failure name the last refill that failed or was refused; refills wait until an Add funds payment
	// succeeds or a new card setup completes. Changing the card in Stripe's portal does not resume them.
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

// Refill is one open refill the worker charges in a single checkout-service call. Its amount and customer are fixed
// when it opens, so every retry under its id, the idempotency key, sends the checkout service the same request; the
// service resolves the customer's card again on each call.
type Refill struct {
	Team        Team
	ID          string
	AmountCents int64
	Customer    string
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
	mode, err := creditPaymentMode(ctx, q)
	if err != nil {
		return s, err
	}
	var failedAt int64
	err = q.QueryRowContext(ctx, `SELECT closed_at, failure FROM credit_checkouts
	    WHERE team = ? AND payment_mode = ? AND refill_blocked = 1 ORDER BY closed_at DESC LIMIT 1`, string(team), mode).Scan(&failedAt, &s.Failure)
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
	  FROM credit_checkouts WHERE team = ? AND payment_mode = ? AND kind = ? AND (COALESCE(closed_at, opened_at) >= ? OR closed_at IS NULL)
	    AND NOT (payment_id = '' AND closed_at IS NOT NULL)`,
		string(team), mode, checkoutKindRefill, now.Add(-refillMonth).UnixNano()).Scan(&open, &s.DayCount, &s.WeekCents, &s.MonthCents); err != nil {
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
	if b.Trust == billingTrustGranted {
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

// safety: a refill that would pass any limit, counting recorded payments and open reservations, is not made; the
// team's credits then run down and Cloud work stops at zero. A charge whose answer was lost counts only once its
// webhook lands (see RecordRefillCharged).
func (s RefillState) limit() string {
	next := s.AmountCents
	switch {
	case next > MaxCardPaymentCents:
		return refillLimitPaymentSize
	case s.Caps.AmountCents > 0 && next > s.Caps.AmountCents:
		return refillLimitStandingSize
	case s.Caps.PerDay > 0 && s.DayCount+1 > s.Caps.PerDay:
		return refillLimitStandingDaily
	case s.Caps.WeekCents > 0 && s.WeekCents+next > s.Caps.WeekCents:
		return refillLimitStandingWeekly
	case s.WeekLimitCents > 0 && s.WeekCents+next > s.WeekLimitCents:
		return refillLimitTeamWeekly
	case s.MonthLimitCents > 0 && s.MonthCents+next > s.MonthLimitCents:
		return refillLimitTeamMonthly
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
	case (s.Enabled || s.AmountCents != 0) && s.AmountCents < MinCardPaymentCents:
		return fmt.Errorf("%w: a refill is at least $%d", ErrInvalidInput, MinCardPaymentCents/100)
	}
	return nil
}

// ErrNoCard refuses turning on refills for a team that never added a card.
var ErrNoCard = errors.New("card billing: save a card before turning on automatic refill")

// refillEligible is the one rule for charging a refill, applied when one opens and before every retry: refills on, no
// failed refill blocking them, no hold, the balance below the threshold and every limit respected. own is the open
// refill a retry would charge, whose reservation the windows already count and whose customer must still be the
// team's; nil asks about opening a new one.
func (st SpendStanding) refillEligible(own *Refill) bool {
	refill := st.Refill
	if own != nil {
		if own.Customer != st.Card.Customer {
			return false
		}
		// An open refill is younger than its key life, so every window already counts it.
		refill.AmountCents = own.AmountCents
		refill.DayCount--
		refill.WeekCents -= own.AmountCents
		refill.MonthCents -= own.AmountCents
	}
	return refill.Enabled && refill.FailedAt.IsZero() && st.Billing.HoldCause == "" && refill.limit() == "" &&
		st.BalanceMicro < refill.ThresholdCents*MicroCreditsPerCent
}

func setRefillTx(ctx context.Context, tx *storeTx, team Team, s RefillSettings, actor auditActor) error {
	if err := s.validate(); err != nil {
		return err
	}
	res, err := tx.execContext(ctx, `UPDATE teams SET refill_enabled = ?, refill_threshold_cents = ?, refill_amount_cents = ?,
	    refill_week_limit_cents = ?, refill_month_limit_cents = ?
	    WHERE name = ? AND (? = 0 OR card_added_at <> 0)`,
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
	return tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: actor, At: tx.now(ctx)}, auditRefillSettings{ReceiptID: change, Enabled: s.Enabled, ThresholdCents: s.ThresholdCents, AmountCents: s.AmountCents, WeekLimitCents: s.WeekLimitCents, MonthLimitCents: s.MonthLimitCents})
}

// EachDueRefill visits every team that opted in or has an open refill. For each, it authorizes in one transaction
// the refill to charge now, opening a new one or keeping an open one only while refillEligible holds, and passes it
// to charge after that transaction commits and before the next team is checked.
//
// safety: authorization is per call, immediately before it, so no team is charged on a check made before another
// team's provider call.
func (o *Operator) EachDueRefill(ctx context.Context, charge func(Refill)) error {
	var teams []Team
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		mode, err := paymentModeTx(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.queryContext(ctx, `SELECT name FROM teams WHERE refill_enabled = 1
		    UNION SELECT team FROM credit_checkouts WHERE payment_mode = ? AND kind = ? AND closed_at IS NULL ORDER BY 1`, mode, checkoutKindRefill)
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
		return err
	}
	return o.EachTeam(ctx, teams, func(t *Tenant) error {
		var r *Refill
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			var err error
			r, err = authorizeRefillTx(ctx, tx, t.team)
			return err
		})
		if err == nil && r != nil {
			charge(*r)
		}
		return err
	})
}

func authorizeRefillTx(ctx context.Context, tx *storeTx, team Team) (*Refill, error) {
	if err := lockPaymentTeamTx(ctx, tx, team); err != nil {
		return nil, err
	}
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	now := tx.now(ctx)
	st, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return nil, err
	}
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	// safety: the checkout service charges whatever it is asked, so an open refill is retried, with its own request
	// under its own key, only while refillEligible holds. One that is not is withdrawn; if an earlier call did charge,
	// its signed payment still credits the closed refill.
	r := Refill{Team: team}
	var opened int64
	err = tx.QueryRowContext(ctx, `SELECT id, amount_minor, opened_at, customer_id
	    FROM credit_checkouts WHERE team = ? AND payment_mode = ? AND kind = ? AND closed_at IS NULL`+tx.forUpdate(), string(team), mode,
		checkoutKindRefill).Scan(&r.ID, &r.AmountCents, &opened, &r.Customer)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		closed := refillExpired
		if now.Sub(time.Unix(0, opened)) < refillKeyLife {
			if st.refillEligible(&r) {
				return &r, nil
			}
			closed = refillWithdrawn
		}
		if _, err := tx.execContext(ctx, `UPDATE credit_checkouts SET closed_at = ?, failure = ? WHERE team = ? AND payment_mode = ? AND id = ?`,
			now.UnixNano(), closed, string(team), mode, r.ID); err != nil {
			return nil, err
		}
		if closed == refillWithdrawn {
			return nil, nil
		}
		if st, err = spendStandingTx(ctx, tx, team, now); err != nil {
			return nil, err
		}
	}
	if !st.refillEligible(nil) {
		return nil, nil
	}
	refill := st.Refill
	r = Refill{Team: team, AmountCents: refill.AmountCents, Customer: st.Card.Customer}
	if r.ID, err = newCreditID("refill"); err != nil {
		return nil, err
	}
	if _, err := l.execContext(ctx, `INSERT INTO credit_checkouts (id, team, session_id, payment_mode, currency, amount_minor,
	    amount_micro, opened_at, expires_at, kind, customer_id)
	    VALUES (?, ?, ?, ?, 'usd', ?, ?, ?, ?, ?, ?)`,
		r.ID, string(team), r.ID, mode, r.AmountCents, r.AmountCents*MicroCreditsPerCent, now.UnixNano(),
		now.Add(refillKeyLife).UnixNano(), checkoutKindRefill, r.Customer); err != nil {
		return nil, err
	}
	if err := tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: auditActor{system: auditController}, At: now}, auditRefill{RefillID: r.ID, AmountCents: r.AmountCents, BalanceMicro: st.BalanceMicro, ThresholdCents: refill.ThresholdCents}); err != nil {
		return nil, err
	}
	return &r, nil
}

// RecordRefillCharged binds the payment intent the checkout service answered for refill r, so the refill counts against
// every limit even when a recheck withdraws it before its signed payment arrives. A lost answer binds nothing; that
// charge counts once its webhook lands, and a refill a withdrawal let through before then is refunded by hand.
func (o *Operator) RecordRefillCharged(ctx context.Context, r Refill, paymentIntent string) error {
	if paymentIntent = strings.TrimSpace(paymentIntent); paymentIntent == "" {
		return fmt.Errorf("%w: a charged refill names its payment intent", ErrInvalidInput)
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		mode, err := paymentModeTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := lockLedgerTx(ctx, tx, r.Team); err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE credit_checkouts SET payment_id = ? WHERE team = ? AND payment_mode = ? AND id = ? AND kind = ? AND payment_id = ''`,
			paymentIntent, string(r.Team), mode, r.ID, checkoutKindRefill)
		return err
	})
}

// RefillNoCard closes a refill the checkout service could not charge because the customer has no card in Stripe.
const RefillNoCard = "no_card"

// RecordRefillFailed closes an unpaid refill with the failure code the checkout service reported, such as no_card,
// authentication_required or a Stripe decline code, stopping refills like a declined payment, and reports the failure
// for owner notification. A signed outcome for the same payment that arrives later finds the refill closed.
func (o *Operator) RecordRefillFailed(ctx context.Context, r Refill, code string) (out PaymentSettlement, err error) {
	if code == "" || len(code) > 64 || strings.Trim(code, "abcdefghijklmnopqrstuvwxyz_") != "" {
		return out, fmt.Errorf("%w: invalid refill failure %q", ErrInvalidInput, code)
	}
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		mode, err := paymentModeTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := lockLedgerTx(ctx, tx, r.Team); err != nil {
			return err
		}
		var checkout paymentCheckout
		err = tx.QueryRowContext(ctx, `SELECT id,team,kind,payment_id,currency,amount_minor,amount_micro,paid_at,closed_at,grant_id FROM credit_checkouts WHERE payment_mode=? AND team=? AND id=? AND kind=?`+tx.forUpdate(), mode, r.Team, r.ID, checkoutKindRefill).Scan(checkout.scan()...)
		if err != nil {
			return err
		}
		if checkout.closed.Valid || checkout.paid.Valid {
			out.Replayed = true
			return nil
		}
		err = closeUnpaidTx(ctx, tx, capability.PaymentEvent{Mode: mode, DeclineCode: code}, &checkout)
		out = checkout.result
		return err
	})
	return out, err
}
