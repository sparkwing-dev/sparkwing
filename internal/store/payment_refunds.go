package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// safety: every reversal obligation survives until its payment can settle in the same transaction.
func recordPaymentConsequenceTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent, kind string) error {
	amount, err := billingProduct(e.AmountMinor, MicroCreditsPerCent)
	if err != nil {
		return err
	}
	var payment, status string
	var recorded int64
	err = tx.QueryRowContext(ctx, `SELECT payment_intent, amount_micro, status FROM payment_refunds
	    WHERE payment_mode = ? AND kind = ? AND refund_id = ?`+tx.forUpdate(), e.Mode, kind, e.ObjectID).Scan(&payment, &recorded, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.execContext(ctx, `INSERT INTO payment_refunds (payment_mode, kind, refund_id, payment_intent, amount_micro,
		    status, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, e.Mode, kind, e.ObjectID, e.PaymentID, amount, e.Status, tx.now(ctx).UnixNano())
		return err
	case err != nil:
		return err
	case payment != e.PaymentID || recorded != amount:
		return authorityError(apierr.Conflict, "payment consequence changed its payment or amount")
	// Stripe can fail a refund it reported succeeded; the failure is recorded so the operator lists it.
	case kind == "refund" && status == cardRefundSucceeded && e.Status == "failed":
	case status == cardRefundSucceeded || status == "failed" || status == "canceled":
		if e.Status == cardRefundSucceeded || e.Status == "failed" || e.Status == "canceled" {
			if status != e.Status {
				return authorityError(apierr.Conflict, "payment consequence has conflicting terminal outcomes")
			}
		}
		return nil
	}
	_, err = tx.execContext(ctx, `UPDATE payment_refunds SET status = ?, updated_at = ? WHERE payment_mode = ? AND kind = ? AND refund_id = ?`,
		e.Status, tx.now(ctx).UnixNano(), e.Mode, kind, e.ObjectID)
	return err
}

// safety: pending payments retain their obligations; refused payments have no grant to reverse.
// Each consequence has one reference, and all reversals together are capped by the original grant.
func applyPaymentConsequencesTx(ctx context.Context, tx *storeTx, team Team, paymentIntent string, attribution paymentAttribution) error {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	var protected sql.NullInt64
	var grant sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT liability_shift, grant_id FROM credit_checkouts
	    WHERE team = ? AND payment_mode = ? AND payment_id = ?`, string(team), mode, paymentIntent).Scan(&protected, &grant)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// safety: a failed attempt may close a pending payment, but only a paid outcome records protection.
	if !protected.Valid {
		return nil
	}
	if !grant.Valid {
		return nil
	}
	rows, err := tx.queryContext(ctx, `SELECT kind, refund_id, amount_micro FROM payment_refunds
	    WHERE payment_mode = ? AND payment_intent = ? AND status = ? ORDER BY updated_at, kind, refund_id`, mode, paymentIntent, cardRefundSucceeded)
	if err != nil {
		return err
	}
	type consequence struct {
		kind, id string
		amount   int64
	}
	var consequences []consequence
	for rows.Next() {
		var r consequence
		if err := rows.Scan(&r.kind, &r.id, &r.amount); err != nil {
			return errors.Join(err, rows.Close())
		}
		consequences = append(consequences, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, r := range consequences {
		if r.kind == "warning" && protected.Int64 == 1 {
			continue
		}
		reference := paymentConsequenceReference(mode, r.kind, r.id)
		actor := auditActor{system: auditPaymentProvider}
		if r.kind == "refund" && r.id == attribution.refund && attribution.actor.subject.ID != "" {
			actor = attribution.actor
		}
		reversed, err := reversePaymentTx(ctx, tx, paymentIntent, reference, actor, r.amount)
		if err != nil {
			return err
		}
		if r.kind == "warning" {
			if err := queueCardRefundTx(ctx, tx, reversed.Team, paymentIntent, reversed.PaidMicro/MicroCreditsPerCent, CardRefusedWarning, tx.now(ctx)); err != nil {
				return err
			}
		}
	}
	return nil
}

// safety: a queued refund that Stripe's recorded refunds already cover in full is settled, so the worker never
// refunds a payment twice whichever refund arrived first.
func (op *Operator) closeRefundedQueueTx(ctx context.Context, tx *storeTx, paymentIntent string) error {
	refunded, err := refundedMicroTx(ctx, tx, paymentIntent)
	if err != nil || refunded == 0 {
		return err
	}
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `UPDATE card_refunds SET status = ? WHERE payment_mode=? AND payment_intent = ? AND status = ?
	    AND amount_micro > 0 AND amount_micro <= ?`, cardRefundSucceeded, mode, paymentIntent, cardRefundDue, refunded)
	return err
}

func refundedMicroTx(ctx context.Context, tx *storeTx, paymentIntent string) (int64, error) {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	var refunded int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_micro), 0) FROM payment_refunds
	    WHERE payment_mode=? AND payment_intent = ? AND kind = 'refund' AND status = ?`, mode, paymentIntent, cardRefundSucceeded).Scan(&refunded)
	return refunded, err
}

func (op *Operator) reconcilePaymentStateTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, payment string, attribution paymentAttribution) error {
	var team Team
	var amount int64
	err := tx.QueryRowContext(ctx, `SELECT team,amount_minor FROM credit_checkouts WHERE payment_mode=? AND payment_id=?`, mode, payment).Scan(&team, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := lockFinancialTeamsTx(ctx, tx, team); err != nil {
		return err
	}
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return err
	}
	rows, err := tx.queryContext(ctx, `SELECT kind,refund_id,status FROM payment_refunds WHERE payment_mode=? AND payment_intent=? ORDER BY kind,refund_id`, mode, payment)
	if err != nil {
		return err
	}
	type state struct{ kind, id, status string }
	var states []state
	for rows.Next() {
		var item state
		if err := rows.Scan(&item.kind, &item.id, &item.status); err != nil {
			return errors.Join(err, rows.Close())
		}
		states = append(states, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range states {
		switch item.kind {
		case "dispute":
			if _, err := holdCreditFreezeTx(ctx, tx, team, paymentConsequenceReference(mode, item.kind, item.id), payment, "disputed", "", tx.now(ctx), auditActor{system: auditPaymentProvider}); err != nil {
				return err
			}
			if item.status == "canceled" {
				if _, err := releaseCreditFreezesTx(ctx, tx, team, paymentConsequenceReference(mode, item.kind, item.id), false, false, "", "", tx.now(ctx), auditActor{system: auditPaymentProvider}); err != nil {
					return err
				}
			}
		case "refund":
			if item.status == cardRefundSucceeded || item.status == "failed" || item.status == "canceled" {
				if err := applyCardRefundTx(ctx, tx, team, payment, item.id, item.status); err != nil {
					return err
				}
			}
		}
	}
	rows, err = tx.queryContext(ctx, `SELECT warning_id FROM payment_warnings WHERE payment_mode=? AND payment_intent=? AND actionable=1 ORDER BY warning_id`, mode, payment)
	if err != nil {
		return err
	}
	var warnings []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		warnings = append(warnings, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range warnings {
		if err := holdForWarningTx(ctx, tx, team, id, payment, tx.now(ctx)); err != nil {
			return err
		}
		if err := recordPaymentConsequenceTx(ctx, tx, capability.PaymentEvent{Mode: mode, ObjectID: id, PaymentID: payment, AmountMinor: amount, Status: cardRefundSucceeded}, "warning"); err != nil {
			return err
		}
	}
	if err := applyPaymentConsequencesTx(ctx, tx, team, payment, attribution); err != nil {
		return err
	}
	return tx.operator.closeRefundedQueueTx(ctx, tx, payment)
}

func paymentConsequenceReference(mode capability.PaymentMode, kind, id string) string {
	if kind == "warning" {
		kind = "fraud_warning"
	}
	return kind + ":" + string(mode) + ":" + id
}

type paymentAttribution struct {
	refund string
	actor  auditActor
}
