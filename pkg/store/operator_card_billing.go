package store

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// DueCardRefunds lists due refunds across the deployment.
func (o *Operator) DueCardRefunds(ctx context.Context, now time.Time) (_ []CardRefundWork, err error) {
	var mode capability.PaymentMode
	if err := o.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		mode, err = paymentModeTx(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	rows, err := o.s.query(ctx, `SELECT team,payment_intent,reason,attempts FROM card_refunds WHERE payment_mode=? AND status=? AND next_attempt_at<=? ORDER BY created_at`, mode, CardRefundDue, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CardRefundWork
	for rows.Next() {
		var w CardRefundWork
		var attempts int64
		if err := rows.Scan(&w.Team, &w.PaymentIntent, &w.Reason, &attempts); err != nil {
			return nil, err
		}
		w.Key = fmt.Sprintf("%s-%d", w.PaymentIntent, attempts)
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkCardRefundMade records the provider response for an exact queued refund attempt.
func (o *Operator) MarkCardRefundMade(
	ctx context.Context, team Team, key, refundID, status string, now time.Time,
) error {
	paymentIntent, attempt, ok := parseRefundKey(key)
	if !ok || refundID == "" || (status != CardRefundPending && status != "requires_action" && status != CardRefundSucceeded && status != "failed" && status != "canceled") {
		return fmt.Errorf("%w: refund key %q", ErrInvalidInput, key)
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		// The mode is configuration, not payment state; the payment lock precedes all association reads.
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
		recorded, err := refundOutcomeTx(ctx, tx, paymentIntent, refundID)
		if err != nil {
			return err
		}
		effective := status
		if recorded != "" {
			if status == CardRefundSucceeded || status == "failed" || status == "canceled" {
				if status != recorded {
					return authorityError(apierr.Conflict, "payment consequence has conflicting terminal outcomes")
				}
			}
			effective = recorded
		} else {
			var amount int64
			if err := tx.QueryRowContext(ctx, `SELECT amount_micro FROM card_refunds WHERE payment_mode=? AND team=? AND payment_intent=?`, mode, team, paymentIntent).Scan(&amount); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE card_refunds SET refund_id=? WHERE payment_mode=? AND team=? AND payment_intent=? AND attempts=? AND status='due'`, refundID, mode, team, paymentIntent, attempt); err != nil {
				return err
			}
			if _, err := applyPaymentStateTx(ctx, tx, capability.PaymentEvent{Kind: capability.PaymentRefund, Mode: mode, ObjectID: refundID, PaymentID: paymentIntent, AmountMinor: amount / MicroCreditsPerCent, Status: status}, "payment_provider"); err != nil {
				return err
			}
		}
		_, err = applyCardRefundTx(ctx, tx, team, paymentIntent, refundID, effective,
			`attempts = ? AND status = 'due'`, []any{attempt}, now)
		return err
	})
}
