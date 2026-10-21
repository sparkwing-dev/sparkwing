package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// MinCardChargeCents is the provider's minimum card payment.
const MinCardChargeCents = 50

const (
	BusinessEventCardAdded     = "card.added"
	BusinessEventCardRefundDue = "card.refund_due"
)

// ErrStaleCardSetup refuses a card setup older than the card already on file.
var ErrStaleCardSetup = errors.New("card billing: a newer card setup is already on file")

// Card is the card a team saved for refills.
type Card struct {
	Customer      string
	PaymentMethod string
	Fingerprint   string
	Brand         string
	Last4         string
	// AddedAt orders setups by completion time, or receipt time when absent.
	AddedAt time.Time
}

// SpendStanding reports a team's standing, saved card, balance and refill.
type SpendStanding struct {
	Billing      BillingStanding
	Card         Card
	BalanceMicro int64
	Refill       RefillState
}

// SpendStanding reports what t may spend as of now.
func (t *Tenant) SpendStanding(ctx context.Context) (out SpendStanding, err error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackOrLog(tx)

	return spendStandingTx(ctx, tx, t.team, tx.now(ctx))
}

func spendStandingTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (SpendStanding, error) {
	var s SpendStanding
	var err error
	if s.Billing, err = trustStandingTx(ctx, q, team, now); err != nil {
		return s, err
	}
	var addedNS int64
	if err := q.QueryRowContext(ctx, `SELECT card_customer, card_payment_method, card_fingerprint, card_brand,
	    card_last4, card_added_at FROM teams WHERE name = ?`, string(team)).Scan(&s.Card.Customer, &s.Card.PaymentMethod,
		&s.Card.Fingerprint, &s.Card.Brand, &s.Card.Last4, &addedNS); err != nil {
		return s, err
	}
	if addedNS > 0 {
		s.Card.AddedAt = time.Unix(0, addedNS)
	}
	if s.BalanceMicro, err = creditBalanceTx(ctx, q, team); err != nil {
		return s, err
	}
	s.Refill, err = refillStateTx(ctx, q, team, s.Billing, now)
	return s, err
}

// safety: admission and every renewal ask this under the team's ledger lock, so two claims racing for the last
// credits cannot both pass, and a hold refuses whatever the balance reads.
func refuseUnfundedTx(ctx context.Context, l *ledgerTx, required int64, runID, nodeID string) error {
	cause, err := teamHoldTx(ctx, l.storeTx, l.team)
	if err != nil {
		return err
	}
	balance, err := l.balance(ctx)
	if err != nil {
		return err
	}
	if cause == "" && balance >= required {
		return nil
	}
	return &InsufficientCreditsError{BalanceMicro: balance, RequiredMicro: required, RunID: runID, NodeID: nodeID, Hold: cause}
}

// safety: a payment is refunded once in full, so a later reason to refund it, or a refund Stripe already
// completed for its whole amount, queues nothing.
func queueCardRefundTx(ctx context.Context, tx *storeTx, team Team, payment string, amountCents int64, reason string, now time.Time) error {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	amount := max(amountCents, 0) * MicroCreditsPerCent
	if _, err := tx.ExecContext(ctx, `UPDATE card_refunds SET amount_micro = ?
	    WHERE payment_mode=? AND team = ? AND payment_intent = ? AND amount_micro = 0`, amount, mode, string(team), payment); err != nil {
		return err
	}
	refunded, err := refundedMicroTx(ctx, tx, payment)
	if err != nil {
		return err
	}
	if amount > 0 && refunded >= amount {
		return tx.operator.closeRefundedQueueTx(ctx, tx, payment)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO card_refunds (payment_mode,payment_intent, team, reason, amount_micro, created_at)
	    VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (payment_mode,team, payment_intent) DO NOTHING`,
		mode, payment, string(team), reason, amount, now.UnixNano())
	if err != nil {
		return err
	}
	if queued, err := res.RowsAffected(); err != nil || queued == 0 {
		return err
	}
	return tx.operator.RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardRefundDue, SubjectID: payment, Actor: "payment_provider", At: now,
		Attrs: map[string]any{"reason": reason, "amount_micro": amount},
	})
}

// A same-payment warning preserves protected credit under a hold; other actionable
// warnings refuse the payment. Non-actionable warnings do not affect settlement.
func paymentWarningTx(ctx context.Context, tx *storeTx, paymentIntent, fingerprint string) (string, error) {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT warning_id FROM payment_warnings
	  WHERE actionable = 1 AND ((payment_mode=? AND payment_intent = ?) OR (fingerprint != '' AND fingerprint = ?))
	  ORDER BY CASE WHEN payment_mode=? AND payment_intent = ? THEN 0 ELSE 1 END, created_at LIMIT 1`, mode, paymentIntent, fingerprint, mode, paymentIntent).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func holdForWarningTx(ctx context.Context, tx *storeTx, team Team, warning, paymentIntent string, now time.Time) error {
	// safety: a length prefix prevents colons in team or provider IDs from colliding.
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	freezeID := "early_fraud_warning:" + string(mode) + ":" + strconv.Itoa(len(team)) + ":" + string(team) + ":" + warning
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return err
	}
	if held, err := rowPresentTx(ctx, tx, `SELECT 1 FROM credit_freezes WHERE team = ? AND dispute_id = ?`,
		string(team), freezeID); err != nil || held {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, created_at)
	    VALUES (?, ?, ?, ?, ?, ?)`,
		freezeID, string(team), paymentIntent, "early fraud warning "+warning, HoldCauseWarning, now.UnixNano()); err != nil {
		return err
	}
	if err := stopHeldAwardsTx(ctx, tx, team); err != nil {
		return err
	}
	return tx.operator.RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: team, Kind: BusinessEventTeamFrozen, SubjectID: freezeID,
		Attrs: map[string]any{"payment_id": paymentIntent, "cause": HoldCauseWarning, "warning_id": warning},
	})
}

// PaymentWarning is an early fraud warning on a payment.
type PaymentWarning struct {
	WarningID     string
	PaymentIntent string
	Fingerprint   string
	// Actionable is Stripe's word that the payment can still be refunded to head off a dispute; only such a warning refuses a payment or holds a team.
	Actionable bool
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

// TeamOwnerEmails returns the verified email addresses of t's owners.
func (t *Tenant) TeamOwnerEmails(ctx context.Context) ([]string, error) {
	return t.teamOwnerEmails(ctx, t.s.query)
}

func (t *Tenant) teamOwnerEmails(ctx context.Context, query func(context.Context, string, ...any) (*sql.Rows, error)) (_ []string, err error) {
	rows, err := query(ctx, `SELECT a.email FROM memberships m JOIN accounts a ON a.id = m.account_id
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
