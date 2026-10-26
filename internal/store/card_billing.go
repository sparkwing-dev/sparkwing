package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// MinCardChargeCents is the provider's minimum card payment.
const MinCardChargeCents = 50

const (
	// CardRefusedWarning refuses a payment that an actionable early fraud warning named before it settled.
	CardRefusedWarning = "warning"

	businessEventCardAdded     = "card.added"
	businessEventCardRefused   = "card.refused"
	businessEventCardRefundDue = "card.refund_due"
)

// errStaleCardSetup refuses a card setup older than the card already on file.
var errStaleCardSetup = errors.New("card billing: a newer card setup is already on file")

// Card records that a card setup completed for the team's Stripe customer. Stripe holds the card and may since have
// removed it; refills charge the customer's default payment method, and a refill without one stops as no_card.
type Card struct {
	Customer string
	// AddedAt orders setups by completion time, or receipt time when absent; zero means no card was ever added.
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
	if err := q.QueryRowContext(ctx, `SELECT card_customer, card_added_at FROM teams WHERE name = ?`,
		string(team)).Scan(&s.Card.Customer, &addedNS); err != nil {
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

// safety: admission and renewal call this with a locked view before writing;
// an unlocked claim probe is advisory and the awarded claim rechecks.
func refuseUnfundedTx(ctx context.Context, view ledgerView, required int64, runID, nodeID string) error {
	cause, err := teamHoldTx(ctx, view.tx, view.team)
	if err != nil {
		return err
	}
	balance, err := view.balance(ctx)
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
	if _, err := tx.execContext(ctx, `UPDATE card_refunds SET amount_micro = ?
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
	res, err := tx.execContext(ctx, `INSERT INTO card_refunds (payment_mode,payment_intent, team, reason, amount_micro, created_at)
	    VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (payment_mode,team, payment_intent) DO NOTHING`,
		mode, payment, string(team), reason, amount, now.UnixNano())
	if err != nil {
		return err
	}
	if queued, err := res.RowsAffected(); err != nil || queued == 0 {
		return err
	}
	return tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: auditActor{system: auditPaymentProvider}, At: now}, auditCard{Reference: digestAudit(payment), Operation: businessEventCardRefundDue, AmountMicro: amount})
}

// paymentWarnedTx reports an actionable early fraud warning that names the payment. Non-actionable warnings do not
// affect settlement, and a warning never reaches another payment: Stripe Radar handles a card used across teams.
func paymentWarnedTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, paymentIntent string) (bool, error) {
	return rowPresentTx(ctx, tx, `SELECT 1 FROM payment_warnings WHERE actionable = 1 AND payment_mode=? AND payment_intent = ?`, mode, paymentIntent)
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
	if _, err := tx.execContext(ctx, `INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, created_at, payment_mode)
	    VALUES (?, ?, ?, ?, ?, ?, ?)`,
		freezeID, string(team), paymentIntent, "early fraud warning "+warning, HoldCauseWarning, now.UnixNano(), mode); err != nil {
		return err
	}
	return tx.operator.recordAudit(ctx, tx, auditContext{At: now, Team: team, Actor: auditActor{system: auditController}}, auditHold{HoldID: digestAudit(freezeID), PaymentID: digestAudit(paymentIntent), WarningID: digestAudit(warning)})
}

const (
	cardRefundDue       = "due"
	cardRefundPending   = "pending"
	cardRefundSucceeded = "succeeded"
)

// CardRefundWork is a payment the worker must refund in full. The payment is the refund's idempotency key, so a
// retry after a lost answer repeats the same refund.
type CardRefundWork struct {
	Team          Team
	PaymentIntent string
	Reason        string
}

// FailedCardRefund is a queued refund Stripe reported failed or canceled; the operator settles it by hand.
type FailedCardRefund struct {
	PaymentIntent string
	RefundID      string
	Status        string
	Reason        string
	AmountMicro   int64
	CreatedAt     time.Time
}

// FailedCardRefunds lists t's refunds that Stripe failed or canceled, newest first.
func (t *Tenant) FailedCardRefunds(ctx context.Context) (_ []FailedCardRefund, err error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	mode, err := creditPaymentMode(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.queryContext(ctx, `SELECT payment_intent,refund_id,status,reason,amount_micro,created_at FROM card_refunds
	    WHERE team=? AND payment_mode=? AND status IN ('failed','canceled') ORDER BY created_at DESC`, string(t.team), mode)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []FailedCardRefund
	for rows.Next() {
		var f FailedCardRefund
		var created int64
		if err := rows.Scan(&f.PaymentIntent, &f.RefundID, &f.Status, &f.Reason, &f.AmountMicro, &created); err != nil {
			return nil, err
		}
		f.CreatedAt = time.Unix(0, created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// TeamOwnerEmails returns the verified email addresses of t's owners.
func (t *Tenant) TeamOwnerEmails(ctx context.Context) ([]string, error) {
	return t.teamOwnerEmails(ctx, t.s.query)
}

func (t *Tenant) teamOwnerEmails(ctx context.Context, query func(context.Context, string, ...any) (rowCursor, error)) (_ []string, err error) {
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
