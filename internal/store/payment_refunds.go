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
		clockTime1, clockErr1 := tx.now(ctx)
		if clockErr1 != nil {
			return clockErr1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO payment_refunds (payment_mode, kind, refund_id, payment_intent, amount_micro,
		    status, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, e.Mode, kind, e.ObjectID, e.PaymentID, amount, e.Status, clockTime1.UnixNano())
		return err
	case err != nil:
		return err
	case payment != e.PaymentID || recorded != amount:
		return authorityError(apierr.Conflict, "payment consequence changed its payment or amount")
	case status == CardRefundSucceeded || status == "failed" || status == "canceled":
		if e.Status == CardRefundSucceeded || e.Status == "failed" || e.Status == "canceled" {
			if status != e.Status {
				return authorityError(apierr.Conflict, "payment consequence has conflicting terminal outcomes")
			}
		}
		return nil
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return clockErr2
	}
	_, err = tx.ExecContext(ctx, `UPDATE payment_refunds SET status = ?, updated_at = ? WHERE payment_mode = ? AND kind = ? AND refund_id = ?`,
		e.Status, clockTime2.UnixNano(), e.Mode, kind, e.ObjectID)
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
	rows, err := tx.QueryContext(ctx, `SELECT kind, refund_id, amount_micro FROM payment_refunds
	    WHERE payment_mode = ? AND payment_intent = ? AND status = ? ORDER BY updated_at, kind, refund_id`, mode, paymentIntent, CardRefundSucceeded)
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
		actor := "payment_provider"
		if r.kind == "refund" && r.id == attribution.refund && attribution.actor != "" {
			actor = attribution.actor
		}
		reversed, err := reversePaymentTx(ctx, tx, paymentIntent, reference, actor, r.amount)
		if err != nil {
			return err
		}
		if r.kind == "warning" {
			clockTime3, clockErr3 := tx.now(ctx)
			if clockErr3 != nil {
				return clockErr3
			}
			if err := queueCardRefundTx(ctx, tx, reversed.Team, paymentIntent, reversed.PaidMicro/MicroCreditsPerCent, CardRefusedWarning, clockTime3); err != nil {
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
	_, err = tx.ExecContext(ctx, `UPDATE card_refunds SET status = ? WHERE payment_mode=? AND payment_intent = ? AND status = ?
	    AND amount_micro > 0 AND amount_micro <= ?`, CardRefundSucceeded, mode, paymentIntent, CardRefundDue, refunded)
	return err
}

func refundedMicroTx(ctx context.Context, tx *storeTx, paymentIntent string) (int64, error) {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	var refunded int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_micro), 0) FROM payment_refunds
	    WHERE payment_mode=? AND payment_intent = ? AND kind = 'refund' AND status = ?`, mode, paymentIntent, CardRefundSucceeded).Scan(&refunded)
	return refunded, err
}

func (op *Operator) reconcilePaymentStateTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, payment string, attribution paymentAttribution) error {
	var team Team
	var amount int64
	var refusalWarning string
	err := tx.QueryRowContext(ctx, `SELECT team,amount_minor,warning_id FROM credit_checkouts WHERE payment_mode=? AND payment_id=?`, mode, payment).Scan(&team, &amount, &refusalWarning)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,refund_id,status FROM payment_refunds WHERE payment_mode=? AND payment_intent=? ORDER BY kind,refund_id`, mode, payment)
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
			clockTime4, clockErr4 := tx.now(ctx)
			if clockErr4 != nil {
				return clockErr4
			}
			if _, err := holdCreditFreezeTx(ctx, tx, team, paymentConsequenceReference(mode, item.kind, item.id), payment, "disputed", "", clockTime4); err != nil {
				return err
			}
			if item.status == "canceled" {
				clockTime5, clockErr5 := tx.now(ctx)
				if clockErr5 != nil {
					return clockErr5
				}
				if _, err := releaseCreditFreezesTx(ctx, tx, team, paymentConsequenceReference(mode, item.kind, item.id), false, false, "", "", clockTime5); err != nil {
					return err
				}
			}
		case "refund":
			if item.status == CardRefundSucceeded || item.status == "failed" || item.status == "canceled" {
				clockTime6, clockErr6 := tx.now(ctx)
				if clockErr6 != nil {
					return clockErr6
				}
				if _, err := applyCardRefundTx(ctx, tx, team, payment, item.id, item.status, `refund_id = ? AND status = 'pending'`, []any{item.id}, clockTime6); err != nil {
					return err
				}
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT warning_id FROM payment_warnings WHERE payment_mode=? AND payment_intent=? AND actionable=1 ORDER BY warning_id`, mode, payment)
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
		if id == refusalWarning {
			refusalWarning = ""
		}
		clockTime7, clockErr7 := tx.now(ctx)
		if clockErr7 != nil {
			return clockErr7
		}
		if err := holdForWarningTx(ctx, tx, team, id, payment, clockTime7); err != nil {
			return err
		}
		if err := recordPaymentConsequenceTx(ctx, tx, capability.PaymentEvent{Mode: mode, ObjectID: id, PaymentID: payment, AmountMinor: amount, Status: CardRefundSucceeded}, "warning"); err != nil {
			return err
		}
	}
	if refusalWarning != "" {
		clockTime8, clockErr8 := tx.now(ctx)
		if clockErr8 != nil {
			return clockErr8
		}
		if err := holdForWarningTx(ctx, tx, team, refusalWarning, payment, clockTime8); err != nil {
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
	actor  string
}
