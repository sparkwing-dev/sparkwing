package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// safety: a refund is kept by its payment whatever order Stripe delivers them in; the refund and the payment's
// settlement each apply it, so a refund that arrives first is taken back in the transaction that grants the payment.
func recordProviderRefundTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) error {
	amount, err := billingProduct(e.AmountMinor, MicroCreditsPerCent)
	if err != nil {
		return err
	}
	var payment, status string
	var recorded int64
	err = tx.QueryRowContext(ctx, `SELECT payment_intent, amount_micro, status FROM payment_refunds
	    WHERE payment_mode = ? AND refund_id = ?`+tx.forUpdate(), e.Mode, e.ObjectID).Scan(&payment, &recorded, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO payment_refunds (payment_mode, refund_id, payment_intent, amount_micro,
		    status, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, e.Mode, e.ObjectID, e.PaymentID, amount, e.Status, tx.now().UnixNano())
		return err
	case err != nil:
		return err
	case payment != e.PaymentID || recorded != amount:
		return authorityError(apierr.Conflict, "refund changed its payment or amount")
	case status == CardRefundSucceeded:
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE payment_refunds SET status = ?, updated_at = ? WHERE payment_mode = ? AND refund_id = ?`,
		e.Status, tx.now().UnixNano(), e.Mode, e.ObjectID)
	return err
}

// safety: each succeeded refund reverses under its own reference, so applying the refunds again after either the
// refund or the payment settles takes back nothing twice.
func applyPaymentRefundsTx(ctx context.Context, tx *storeTx, paymentIntent string) error {
	if _, funded, err := paidGrantForPaymentTx(ctx, tx, paymentIntent); err != nil || !funded {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT payment_mode, refund_id, amount_micro FROM payment_refunds
	    WHERE payment_intent = ? AND status = ? ORDER BY updated_at, refund_id`, paymentIntent, CardRefundSucceeded)
	if err != nil {
		return err
	}
	type refund struct {
		mode, id string
		amount   int64
	}
	var refunds []refund
	for rows.Next() {
		var r refund
		if err := rows.Scan(&r.mode, &r.id, &r.amount); err != nil {
			return errors.Join(err, rows.Close())
		}
		refunds = append(refunds, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, r := range refunds {
		if _, err := reversePaymentTx(ctx, tx, paymentIntent, "refund:"+r.mode+":"+r.id, "payment_provider", r.amount); err != nil {
			return err
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
	    WHERE payment_intent = ? AND status = ?`, paymentIntent, CardRefundSucceeded).Scan(&refunded)
	return refunded, err
}

func refundOutcomeTx(ctx context.Context, tx *storeTx, paymentIntent, refundID string) (string, error) {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return "", err
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM payment_refunds WHERE payment_mode = ? AND payment_intent = ? AND refund_id = ?`,
		mode, paymentIntent, refundID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil || status != CardRefundSucceeded && status != "failed" && status != "canceled" {
		return "", err
	}
	return status, nil
}
