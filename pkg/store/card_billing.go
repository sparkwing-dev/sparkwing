package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// MinCardChargeCents is the provider's minimum card charge.
	MinCardChargeCents = 50
	// SpendWindowDays is the owner budget's rolling window in UTC days.
	SpendWindowDays = 30
)

const (
	SpendLimitBalance       = "balance"
	SpendLimitBudget        = "budget"
	SpendLimitChargeFailed  = "charge_failed"
	SpendLimitChargePending = "charge_pending"
	SpendLimitHeld          = "held"
)

const (
	BusinessEventCardAdded      = "card.added"
	BusinessEventCardCharged    = "card.charge_paid"
	BusinessEventCardFailed     = "card.charge_failed"
	BusinessEventCardRefundDue  = "card.refund_due"
	BusinessEventCardWarned     = "card.payment_warned"
	BusinessEventBudgetAlert    = "billing.budget_alert"
	BusinessEventCardChargeOpen = "card.charge_opened"
)

const (
	CardChargeOpen    = "open"
	CardChargePaid    = "paid"
	CardAttemptLive   = "live"
	CardAttemptPaid   = "succeeded"
	CardAttemptFailed = "failed"
	// CardAttemptWarned is an attempt whose payment drew an early fraud warning; it is refunded and grants nothing.
	CardAttemptWarned = "warned"

	CardAttemptOffSession = "off_session"
	CardAttemptRecovery   = "recovery"
)

const (
	FreezeCauseDecline = "decline"
	FreezeCauseWarning = "early_fraud_warning"
)

// safety: past the last retry only the owner's "pay now" or a new card
// tries a failed charge again, so a dead card is not hammered.
var cardRetryAfter = []time.Duration{24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour}

// safety: this outlives the 31-minute Checkout session a recovery attempt
// opens, after which the session can no longer be paid.
const recoveryAttemptLife = 35 * time.Minute

const stalledAttemptAge = 2 * time.Minute

// safety: Stripe keeps an idempotency key for 24 hours. An attempt that never
// learned its payment intent by then is closed and a new one minted, rather
// than reusing a key Stripe may have forgotten; its intent, if Stripe made
// one, was never confirmed, so it moved no money.
const cardKeyLife = 23 * time.Hour

const dayNS = int64(24 * time.Hour)

func addSpendTx(ctx context.Context, tx *storeTx, team Team, atNS, amount int64) error {
	if amount == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO team_spend_days(team,day,amount_micro) VALUES (?,?,?) ON CONFLICT(team,day) DO UPDATE SET amount_micro=team_spend_days.amount_micro+excluded.amount_micro`, team, atNS/dayNS, amount)
	return err
}

func teamCardTx(ctx context.Context, tx *storeTx, team Team) (string, error) {
	var fingerprint string
	err := tx.QueryRowContext(ctx, `SELECT card_fingerprint FROM teams WHERE name = ?`, string(team)).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fingerprint, err
}

// Card is the card a team's usage is billed to.
type Card struct {
	Customer      string
	PaymentMethod string
	Fingerprint   string
	Brand         string
	Last4         string
	// AddedAt orders setups by completion time, or receipt time when absent.
	AddedAt time.Time
}

// CardCharge is one obligation billed to a team's card.
type CardCharge struct {
	ID            string
	Team          Team
	AmountMicro   int64
	Cause         string
	State         string
	Failures      int64
	DeclineCode   string
	NextAttemptAt time.Time
	PaymentIntent string
	OpenedAt      time.Time
}

// SpendStanding reports balance, collection state and the owner's budget.
type SpendStanding struct {
	Billing         BillingStanding
	Card            Card
	OpenCharge      *CardCharge
	BalanceMicro    int64
	RungMicro       int64
	BudgetMicro     int64
	Spent30dMicro   int64
	SpentTodayMicro int64
}

// CardBilled reports whether usage can be collected from the team's card.
func (s SpendStanding) CardBilled() bool { return s.Card.PaymentMethod != "" }

// ChargeFailed reports an open collection with a failed attempt.
func (s SpendStanding) ChargeFailed() bool { return s.OpenCharge != nil && s.OpenCharge.Failures > 0 }

// RunningWorkContinues reports whether awarded Cloud work finishes while new admissions wait: any hold or collection in progress.
func (s SpendStanding) RunningWorkContinues() bool {
	return s.Billing.Tier == "Held" || s.OpenCharge != nil || s.CardBilled() && -s.BalanceMicro >= s.RungMicro
}

// Headroom reports admission room under standing, collection and the owner's budget.
func (s SpendStanding) Headroom() (int64, string) {
	if s.Billing.Tier == "Held" && s.Billing.HoldCause != FreezeCauseDecline && s.Billing.HoldCause != "" {
		return 0, SpendLimitHeld
	}
	if s.Billing.Tier == "Held" || s.ChargeFailed() {
		return 0, SpendLimitChargeFailed
	}
	if s.OpenCharge != nil {
		return 0, SpendLimitChargePending
	}
	room, limit := s.BalanceMicro, SpendLimitBalance
	if s.CardBilled() {
		room = 1<<63 - 1
		if -s.BalanceMicro >= s.RungMicro {
			return 0, SpendLimitChargePending
		}
	}
	if s.BudgetMicro > 0 && s.BudgetMicro-s.Spent30dMicro < room {
		return s.BudgetMicro - s.Spent30dMicro, SpendLimitBudget
	}
	return room, limit
}

// SpendStanding reports what t may still spend as of now.
func (t *Tenant) SpendStanding(ctx context.Context, now time.Time) (SpendStanding, error) {
	return spendStandingTx(ctx, storeRowQuerier{t.s}, t.team, now)
}

const spendWindowSQL = `SELECT COALESCE(SUM(amount_micro), 0),
    COALESCE(SUM(CASE WHEN day = ? THEN amount_micro ELSE 0 END), 0)
  FROM %s WHERE %s = ? AND day >= ?`

func spendStandingTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (SpendStanding, error) {
	var s SpendStanding
	var err error
	if s.Billing, err = trustStandingTx(ctx, q, team, now); err != nil {
		return s, err
	}
	var addedNS, rungIndex, budgetCents int64
	if err := q.QueryRowContext(ctx, `SELECT card_customer, card_payment_method, card_fingerprint, card_brand,
	    card_last4, card_added_at, billing_rung_index, billing_budget_cents
	    FROM teams WHERE name = ?`, string(team)).Scan(&s.Card.Customer, &s.Card.PaymentMethod,
		&s.Card.Fingerprint, &s.Card.Brand, &s.Card.Last4, &addedNS, &rungIndex, &budgetCents); err != nil {
		return s, err
	}
	if addedNS > 0 {
		s.Card.AddedAt = time.Unix(0, addedNS)
	}
	if s.OpenCharge, err = openCardChargeTx(ctx, q, team); err != nil {
		return s, err
	}
	today := now.UnixNano() / dayNS
	if err := q.QueryRowContext(ctx, fmt.Sprintf(spendWindowSQL, "team_spend_days", "team"),
		today, string(team), today-SpendWindowDays).Scan(&s.Spent30dMicro, &s.SpentTodayMicro); err != nil {
		return s, err
	}
	if s.BalanceMicro, err = creditBalanceTx(ctx, q, team); err != nil {
		return s, err
	}
	if rungIndex < 0 || rungIndex >= int64(len(billingPolicy)) {
		return s, fmt.Errorf("invalid billing rung index %d", rungIndex)
	}
	s.RungMicro = billingPolicy[rungIndex].rungCents * MicroCreditsPerCent
	s.BudgetMicro = budgetCents * MicroCreditsPerCent
	return s, nil
}

func openCardChargeTx(ctx context.Context, q rowQuerier, team Team) (*CardCharge, error) {
	c := CardCharge{Team: team}
	var next, opened int64
	err := q.QueryRowContext(ctx, `SELECT id, amount_micro, cause, state, failures, decline_code, next_attempt_at,
	    payment_intent, opened_at FROM card_charges WHERE team = ? AND state = ?`, string(team), CardChargeOpen).
		Scan(&c.ID, &c.AmountMicro, &c.Cause, &c.State, &c.Failures, &c.DeclineCode, &next, &c.PaymentIntent, &opened)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.NextAttemptAt, c.OpenedAt = time.Unix(0, next), time.Unix(0, opened)
	return &c, nil
}

// safety: every claim, and every charge of running work, asks this under the
// ledger lock, so two claims racing for the last room cannot both pass it.
func refuseAboveHeadroomTx(
	ctx context.Context, tx *storeTx, team Team, required int64, now time.Time, runID, nodeID string,
) error {
	s, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return err
	}
	room, limit := s.Headroom()
	if room >= required {
		return nil
	}
	out := &InsufficientCreditsError{
		BalanceMicro: s.BalanceMicro, RequiredMicro: required, RunID: runID, NodeID: nodeID, Limit: limit,
	}
	if s.Billing.Tier == "Held" {
		return holdRefusal(s.Billing.HoldCause, out)
	}
	return out
}

func headroomTx(ctx context.Context, tx *storeTx, team Team, now time.Time) (int64, error) {
	s, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return 0, err
	}
	room, _ := s.Headroom()
	return room, nil
}

// ErrStaleCardSetup refuses a card setup older than the card already on file.
var ErrStaleCardSetup = errors.New("card billing: a newer card setup is already on file")

func setSpendBudgetTx(ctx context.Context, tx *storeTx, team Team, cents int64) error {
	if cents < 0 || cents > MaxCreditGrantMicro/MicroCreditsPerCent {
		return fmt.Errorf("%w: a budget is between $0 and $%d", ErrInvalidInput, MaxCreditGrantMicro/MicroCreditsPerCent/100)
	}
	_, err := tx.ExecContext(ctx, `UPDATE teams SET billing_budget_cents = ? WHERE name = ?`, cents, string(team))
	return err
}

// CardChargeWork is one Stripe call the payment worker owes for an open charge, under the attempt's id.
type CardChargeWork struct {
	Team          Team
	ChargeID      string
	AttemptID     string
	AmountCents   int64
	Customer      string
	PaymentMethod string
	PaymentIntent string
}

// BudgetAlert is a budget threshold a team crossed this month, reported once.
type BudgetAlert struct {
	Team          Team
	Percent       int64
	Spent30dMicro int64
	BudgetMicro   int64
}

type billedTeam struct {
	name   Team
	carded bool
}

// safety: only a team with a card gets a charge opened, since a charge with
// no card to try would stay open and block the team's deletion; a team with
// only a budget is visited for its alerts.
func billedTeamsTx(ctx context.Context, tx *storeTx) (_ []billedTeam, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, card_payment_method != '' FROM teams
	    WHERE card_payment_method != '' OR billing_budget_cents > 0 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []billedTeam
	for rows.Next() {
		var t billedTeam
		if err := rows.Scan(&t.name, &t.carded); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// safety: a dispute, fraud warning or operator hold stops the card being
// charged; a decline's own hold does not, since the retries are what lift it.
const blockingHoldSQL = `SELECT 1 FROM credit_freezes f WHERE f.team = ? AND f.released_at IS NULL AND f.cause != ?`

// safety: a team with a card owes whatever its balance reads below zero; the
// charge opens at the rung, on the first of the month for a debt carried
// over it, and at once for a team that is no longer card-billed, so a
// revocation or a hold settles what is owed.
func decideCardChargeTx(ctx context.Context, tx *storeTx, team Team, now time.Time) error {
	st, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return err
	}
	nowNS := now.UnixNano()
	var owedSince, paidCharges int64
	if err := tx.QueryRowContext(ctx, `SELECT card_owed_since,(SELECT COUNT(*) FROM card_charges WHERE team=teams.name AND state=?) FROM teams WHERE name = ?`, CardChargePaid, string(team)).
		Scan(&owedSince, &paidCharges); err != nil {
		return err
	}
	switch {
	case st.BalanceMicro >= 0 && owedSince != 0:
		owedSince = 0
	case st.BalanceMicro < 0 && owedSince == 0:
		owedSince = nowNS
	}
	if _, err := tx.ExecContext(ctx, `UPDATE teams SET card_owed_since = ? WHERE name = ?`,
		owedSince, string(team)); err != nil {
		return err
	}
	owedCents := -st.BalanceMicro / MicroCreditsPerCent
	if st.OpenCharge != nil || owedCents < MinCardChargeCents {
		return nil
	}
	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC).UnixNano()
	cause := ""
	switch {
	case !st.CardBilled():
		cause = "settle"
	case -st.BalanceMicro >= st.RungMicro:
		cause = "rung"
	case paidCharges == 0 && owedSince > 0 && !now.Before(time.Unix(0, owedSince).Add(7*24*time.Hour)):
		cause = "first_week"
	case owedSince < monthStart:
		cause = "month_end"
	default:
		return nil
	}
	if held, err := rowPresentTx(ctx, tx, blockingHoldSQL, string(team), FreezeCauseDecline); err != nil || held {
		return err
	}
	id, err := newCreditID("cardcharge")
	if err != nil {
		return err
	}
	amount := owedCents * MicroCreditsPerCent
	if _, err := tx.ExecContext(ctx, `INSERT INTO card_charges (id, team, amount_micro, cause, state,
	    next_attempt_at, opened_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, string(team), amount, cause, CardChargeOpen, nowNS, nowNS); err != nil {
		return fmt.Errorf("card billing: open charge: %w", err)
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardChargeOpen, SubjectID: id, Actor: "system", At: now,
		Attrs: map[string]any{"amount_micro": amount, "cause": cause},
	})
}

func dueChargesTx(ctx context.Context, tx *storeTx, now time.Time) (_ []CardChargeWork, err error) {
	nowNS := now.UnixNano()
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.team, c.amount_micro, t.card_customer, t.card_payment_method,
	    COALESCE(a.id, ''), COALESCE(a.payment_intent, '')
	  FROM card_charges c JOIN teams t ON t.name = c.team
	  LEFT JOIN card_attempts a ON a.charge_id = c.id AND a.status = ?
	  WHERE c.state = ? AND t.card_payment_method != ''
	    AND ((a.id IS NULL AND c.next_attempt_at <= ?)
	      OR (a.kind = ? AND a.updated_at < ?))
	    AND NOT EXISTS (SELECT 1 FROM credit_freezes f
	                     WHERE f.team = c.team AND f.released_at IS NULL AND f.cause != ?)
	  ORDER BY c.opened_at`,
		CardAttemptLive, CardChargeOpen, nowNS, CardAttemptOffSession, now.Add(-stalledAttemptAge).UnixNano(),
		FreezeCauseDecline)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var work []CardChargeWork
	for rows.Next() {
		var w CardChargeWork
		var amount int64
		if err := rows.Scan(&w.ChargeID, &w.Team, &amount, &w.Customer, &w.PaymentMethod, &w.AttemptID,
			&w.PaymentIntent); err != nil {
			return nil, err
		}
		w.AmountCents = amount / MicroCreditsPerCent
		work = append(work, w)
	}
	return work, rows.Err()
}

func startDueAttemptsTx(ctx context.Context, tx *storeTx, now time.Time) (_ []CardChargeWork, err error) {
	nowNS := now.UnixNano()
	work, err := dueChargesTx(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	for i := range work {
		if work[i].AttemptID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET updated_at = ? WHERE id = ?`,
				nowNS, work[i].AttemptID); err != nil {
				return nil, err
			}
			continue
		}
		if work[i].AttemptID, err = insertCardAttemptTx(ctx, tx, work[i].Team, work[i].ChargeID,
			CardAttemptOffSession, nowNS); err != nil {
			return nil, err
		}
	}
	return work, nil
}

func insertCardAttemptTx(ctx context.Context, tx *storeTx, team Team, chargeID, kind string, nowNS int64) (string, error) {
	id, err := newCreditID("attempt")
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO card_attempts (id, team, charge_id, kind, status, created_at,
	    updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, string(team), chargeID, kind, CardAttemptLive, nowNS, nowNS); err != nil {
		return "", fmt.Errorf("card billing: start attempt: %w", err)
	}
	return id, nil
}

// ErrNoPayableCharge is returned when an owner asks to pay a charge that is not open, or one with an attempt already in flight.
var ErrNoPayableCharge = errors.New("card billing: no charge is waiting for payment")

func (t *Tenant) startRecoveryAttemptTx(ctx context.Context, tx *storeTx, now time.Time) (CardChargeWork, error) {
	charge, err := openCardChargeTx(ctx, tx, t.team)
	if err != nil {
		return CardChargeWork{}, err
	}
	if charge == nil {
		return CardChargeWork{}, ErrNoPayableCharge
	}
	w := CardChargeWork{Team: t.team, ChargeID: charge.ID, AmountCents: charge.AmountMicro / MicroCreditsPerCent}
	if err := tx.QueryRowContext(ctx, `SELECT card_customer FROM teams WHERE name = ?`, string(t.team)).
		Scan(&w.Customer); err != nil {
		return CardChargeWork{}, err
	}
	live, err := rowPresentTx(ctx, tx, `SELECT 1 FROM card_attempts WHERE team = ? AND charge_id = ? AND status = ?`,
		string(t.team), charge.ID, CardAttemptLive)
	if err != nil {
		return CardChargeWork{}, err
	}
	if live {
		return CardChargeWork{}, ErrNoPayableCharge
	}
	if w.AttemptID, err = insertCardAttemptTx(ctx, tx, t.team, charge.ID, CardAttemptRecovery, now.UnixNano()); err != nil {
		return CardChargeWork{}, err
	}
	return w, nil
}

// CardPayment is a succeeded Stripe payment of a card charge.
type CardPayment struct {
	Team          Team
	ChargeID      string
	AttemptID     string
	PaymentIntent string
	AmountCents   int64
	// Fingerprint is the card that paid, which a pay-now Checkout may take from another card than the one on file.
	Fingerprint string
}

type cardAttemptRow struct {
	team, chargeID, kind, status, paymentIntent string
}

// safety: a payment already granted, queued for refund or refused over a
// warning has been settled; its redelivery changes nothing.
func cardPaymentSeenTx(ctx context.Context, tx *storeTx, team Team, paymentIntent string) (bool, error) {
	grant, found, err := paidGrantForPaymentTx(ctx, tx, paymentIntent)
	if err == nil && found && grant.Team != team {
		return false, ErrCreditGrantConflict
	}
	if err != nil || found {
		return found, err
	}
	return rowPresentTx(ctx, tx, `SELECT 1 FROM card_attempts WHERE team = ? AND payment_intent = ? AND status = ?`,
		string(team), paymentIntent, CardAttemptWarned)
}

func insertCardGrantTx(ctx context.Context, tx *storeTx, team Team, amount int64, paymentIntent string, nowNS int64) error {
	id, err := newCreditID("grant")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses,
	    created_by, created_at) VALUES (?, ?, ?, ?, ?, '', 'card', ?)`,
		string(team), id, CreditGrantPaid, amount, paymentIntent, nowNS); err != nil {
		return fmt.Errorf("card billing: grant payment: %w", err)
	}
	return nil
}

func queueCardRefundTx(ctx context.Context, tx *storeTx, team Team, p CardPayment, reason string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO card_refunds (payment_intent, team, reason, created_at)
	    VALUES (?, ?, ?, ?) ON CONFLICT (team, payment_intent) DO NOTHING`,
		p.PaymentIntent, string(team), reason, now.UnixNano()); err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardRefundDue, SubjectID: p.PaymentIntent, Actor: "card", At: now,
		Attrs: map[string]any{
			"charge_id": p.ChargeID, "attempt_id": p.AttemptID, "reason": reason,
			"amount_micro": p.AmountCents * MicroCreditsPerCent,
		},
	})
}

// safety: matched by payment and by the card that paid, so a warning that came
// first, or one on the same card, stops the grant while another card's payment
// is granted; a non-actionable warning is never refunded, so it stops nothing.
func paymentWarningTx(ctx context.Context, tx *storeTx, paymentIntent, fingerprint string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT warning_id FROM payment_warnings
	  WHERE actionable = 1 AND (payment_intent = ? OR (fingerprint != '' AND fingerprint = ?))
	  ORDER BY created_at LIMIT 1`, paymentIntent, fingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// WarnedCardPayment is a card payment whose payment or card drew an actionable early fraud warning.
type WarnedCardPayment struct {
	WarningID string
	// Repays is true when the payment settled the open charge as a debt repayment, even one a refunded reservation had already cleared.
	Repays bool
	// RepaidMicro is the debt the payment repaid; zero when it granted nothing.
	RepaidMicro int64
	// UnappliedMicro is the rest of the payment, which the ledger does not hold: returning it is the operator's decision.
	UnappliedMicro int64
}

// safety: this prefix marks the reversal keeping part of a payment off the
// balance and each release of it; no caller reference may carry it, so a
// refund never mistakes its own reference for that rest.
const unappliedPrefix = "unapplied:"

// safety: the payment's grant carries all of it so a refund or a dispute can
// find it, and this reversal keeps the unapplied rest off the balance; a
// refund first releases this rest, so returning it leaves the repayment alone.
func insertUnappliedTx(ctx context.Context, tx *storeTx, team Team, paymentIntent string, micro, nowNS int64) error {
	id, err := newCreditID("grant")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses,
	    created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, 'card', ?)`,
		string(team), id, CreditGrantReversal, -micro, unappliedPrefix+paymentIntent, paymentIntent, nowNS)
	return err
}

// safety: debt is usage already consumed, so any card may repay it; a pay-now
// payment of the open charge by a card warned only elsewhere repays what the
// team still owes now, which a refunded reservation may have lowered since the
// charge opened, and never becomes prepaid credit.
func warnedCardRepaidTx(
	ctx context.Context, tx *storeTx, team Team, kind, paymentIntent string, payable bool, amount int64,
) (repays bool, repaid int64, err error) {
	if kind != CardAttemptRecovery || !payable {
		return false, 0, nil
	}
	if own, err := rowPresentTx(ctx, tx, `SELECT 1 FROM payment_warnings WHERE actionable = 1 AND payment_intent = ?`,
		paymentIntent); err != nil || own {
		return false, 0, err
	}
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return false, 0, err
	}
	return true, min(amount, max(0, -balance)), nil
}

func settleWarnedTx(ctx context.Context, tx *storeTx, team Team, p CardPayment, w WarnedCardPayment, now time.Time) error {
	if !w.Repays {
		if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, updated_at = ?
		    WHERE team = ? AND id = ?`, CardAttemptWarned, p.PaymentIntent, now.UnixNano(), string(team), p.AttemptID); err != nil {
			return err
		}
	}
	if err := holdForWarningTx(ctx, tx, team, w.WarningID, p.PaymentIntent, now); err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardWarned, SubjectID: p.PaymentIntent, Actor: "card", At: now,
		Attrs: map[string]any{
			"charge_id": p.ChargeID, "attempt_id": p.AttemptID, "warning_id": w.WarningID,
			"repaid_micro": w.RepaidMicro, "unapplied_micro": w.UnappliedMicro,
		},
	})
}

// safety: callers hold the ledger lock, so the check and the insert cannot
// interleave with another hold of the same warning.
func holdForWarningTx(ctx context.Context, tx *storeTx, team Team, warning, paymentIntent string, now time.Time) error {
	// safety: a length prefix prevents colons in team or provider IDs from colliding.
	freezeID := fmt.Sprintf("early_fraud_warning:%d:%s:%s", len(team), team, warning)
	if held, err := rowPresentTx(ctx, tx, `SELECT 1 FROM credit_freezes
	    WHERE team = ? AND cause = ? AND dispute_id = ?`,
		string(team), FreezeCauseWarning, freezeID); err != nil || held {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, created_at)
	    VALUES (?, ?, ?, ?, ?, ?)`,
		freezeID, string(team), paymentIntent, "early fraud warning "+warning, FreezeCauseWarning, now.UnixNano()); err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: team, Kind: BusinessEventTeamFrozen, SubjectID: freezeID,
		Attrs: map[string]any{"payment_id": paymentIntent, "cause": FreezeCauseWarning, "warning_id": warning},
	})
}

// PaymentWarning is an early fraud warning on a payment.
type PaymentWarning struct {
	WarningID     string
	PaymentIntent string
	Fingerprint   string
	// Actionable is Stripe's word that the payment can still be refunded to head off a dispute; only such a warning stops a grant or holds a team.
	Actionable bool
}

// ErrPaymentWarned refuses granting a payment that drew an early fraud warning; the warning's refund returns the money.
var ErrPaymentWarned = errors.New("credits: the payment drew an early fraud warning and is refunded, not granted")

func refuseWarnedPaymentTx(ctx context.Context, tx *storeTx, paymentIntent string) error {
	if paymentIntent == "" {
		return nil
	}
	warned, err := rowPresentTx(ctx, tx, `SELECT 1 FROM payment_warnings WHERE actionable = 1 AND payment_intent = ?`,
		paymentIntent)
	if err != nil || !warned {
		return err
	}
	return ErrPaymentWarned
}

const (
	CardRefundDue       = "due"
	CardRefundPending   = "pending"
	CardRefundSucceeded = "succeeded"
)

// safety: a failed refund is retried on a widening schedule, and never
// dropped, because until it succeeds the payer holds neither the money nor
// credits for it.
var cardRefundRetryAfter = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

// CardRefundWork is a payment the worker must refund in full.
type CardRefundWork struct {
	Team          Team
	PaymentIntent string
	Reason        string
	Key           string
}

func parseRefundKey(key string) (string, int64, bool) {
	i := strings.LastIndexByte(key, '-')
	if i <= 0 {
		return "", 0, false
	}
	attempt, err := strconv.ParseInt(key[i+1:], 10, 64)
	return key[:i], attempt, err == nil && attempt >= 0
}

// safety: the business event's unique subject is what reports a threshold
// once per team per month, whichever controller's worker gets there first.
func budgetAlertTx(ctx context.Context, tx *storeTx, team Team, now time.Time) (*BudgetAlert, error) {
	st, err := spendStandingTx(ctx, tx, team, now)
	if err != nil || st.BudgetMicro <= 0 {
		return nil, err
	}
	pct := int64(0)
	for _, p := range []int64{100, 80, 50} {
		if st.Spent30dMicro*100 >= st.BudgetMicro*p {
			pct = p
			break
		}
	}
	if pct == 0 {
		return nil, nil
	}
	subject := fmt.Sprintf("%s:%d", now.UTC().Format("2006-01"), pct)
	if seen, err := rowPresentTx(ctx, tx, `SELECT 1 FROM business_events WHERE team = ? AND kind = ? AND subject_id = ?`,
		string(team), BusinessEventBudgetAlert, subject); err != nil || seen {
		return nil, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventBudgetAlert, SubjectID: subject, Actor: "system", At: now,
		Attrs: map[string]any{"percent": pct, "spent_micro": st.Spent30dMicro, "budget_micro": st.BudgetMicro},
	}); err != nil {
		return nil, err
	}
	return &BudgetAlert{Team: team, Percent: pct, Spent30dMicro: st.Spent30dMicro, BudgetMicro: st.BudgetMicro}, nil
}

// TeamOwnerEmails returns the verified email addresses of t's owners.
func (t *Tenant) TeamOwnerEmails(ctx context.Context) (_ []string, err error) {
	rows, err := t.s.query(ctx, `SELECT a.email FROM memberships m JOIN accounts a ON a.id = m.account_id
	  WHERE m.team = ? AND m.role = 'owner' AND a.email_verified = 1 ORDER BY a.email`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}

func teamHasCardTx(ctx context.Context, tx *storeTx, team Team) (bool, error) {
	return rowPresentTx(ctx, tx, `SELECT 1 FROM teams WHERE name = ? AND card_payment_method != ''`, string(team))
}
