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
		_, err = tx.ExecContext(ctx, `INSERT INTO payment_refunds (payment_mode, kind, refund_id, payment_intent, amount_micro,
		    status, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, e.Mode, kind, e.ObjectID, e.PaymentID, amount, e.Status, tx.now().UnixNano())
		return err
	case err != nil:
		return err
	case payment != e.PaymentID || recorded != amount:
		return authorityError(apierr.Conflict, "payment consequence changed its payment or amount")
	case status == CardRefundSucceeded:
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE payment_refunds SET status = ?, updated_at = ? WHERE payment_mode = ? AND kind = ? AND refund_id = ?`,
		e.Status, tx.now().UnixNano(), e.Mode, kind, e.ObjectID)
	return err
}

// safety: pending payments retain their obligations; refused payments have no grant to reverse.
// Each consequence has one reference, and all reversals together are capped by the original grant.
func applyPaymentConsequencesTx(ctx context.Context, tx *storeTx, paymentIntent string) error {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	var protected sql.NullInt64
	var grant sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT liability_shift, grant_id FROM credit_checkouts
	    WHERE payment_mode = ? AND payment_id = ?`, mode, paymentIntent).Scan(&protected, &grant)
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
		reference := "refund:" + string(mode) + ":" + r.id
		switch r.kind {
		case "dispute":
			reference = r.id
		case "warning":
			if protected.Int64 == 1 {
				continue
			}
			reference = "fraud_warning:" + r.id
		}
		reversed, err := reversePaymentTx(ctx, tx, paymentIntent, reference, "payment_provider", r.amount)
		if err != nil {
			return err
		}
		if r.kind == "warning" {
			if err := queueCardRefundTx(ctx, tx, reversed.Team, paymentIntent, reversed.PaidMicro/MicroCreditsPerCent, CardRefusedWarning, tx.now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// safety: a queued refund that Stripe's recorded refunds already cover in full is settled, so the worker never
// refunds a payment twice whichever refund arrived first.
func closeRefundedQueueTx(ctx context.Context, tx *storeTx, paymentIntent string) error {
	refunded, err := refundedMicroTx(ctx, tx, paymentIntent)
	if err != nil || refunded == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE card_refunds SET status = ? WHERE payment_intent = ? AND status = ?
	    AND amount_micro > 0 AND amount_micro <= ?`, CardRefundSucceeded, paymentIntent, CardRefundDue, refunded)
	return err
}

func refundedMicroTx(ctx context.Context, tx *storeTx, paymentIntent string) (int64, error) {
	var refunded int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_micro), 0) FROM payment_refunds
	    WHERE payment_intent = ? AND kind = 'refund' AND status = ?`, paymentIntent, CardRefundSucceeded).Scan(&refunded)
	return refunded, err
}

func refundOutcomeTx(ctx context.Context, tx *storeTx, paymentIntent, refundID string) (string, error) {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return "", err
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM payment_refunds WHERE payment_mode = ? AND kind = 'refund' AND payment_intent = ? AND refund_id = ?`,
		mode, paymentIntent, refundID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil || status != CardRefundSucceeded && status != "failed" && status != "canceled" {
		return "", err
	}
	return status, nil
}
