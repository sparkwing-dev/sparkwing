package store

import (
	"context"
	"database/sql"
	"errors"
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

// MarkCardRefundMade binds the provider refund ID to an exact queued attempt.
// Only verified callbacks establish the refund's amount and outcome.
func (o *Operator) MarkCardRefundMade(ctx context.Context, team Team, key, refundID string) error {
	paymentIntent, attempt, ok := parseRefundKey(key)
	if !ok || refundID == "" {
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
		if found, err := rowPresentTx(ctx, tx, `SELECT 1 FROM card_refunds WHERE payment_mode=? AND team=? AND payment_intent=?`, mode, team, paymentIntent); err != nil {
			return err
		} else if !found {
			return authorityError(apierr.NotFound, "queued refund is unavailable")
		}
		var recordedPayment string
		err = tx.QueryRowContext(ctx, `SELECT payment_intent FROM payment_refunds WHERE payment_mode=? AND kind='refund' AND refund_id=?`, mode, refundID).Scan(&recordedPayment)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && recordedPayment != paymentIntent {
			return authorityError(apierr.Conflict, "refund belongs to another payment")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE card_refunds SET refund_id=?, status='pending' WHERE payment_mode=? AND team=? AND payment_intent=? AND attempts=? AND status='due'`, refundID, mode, team, paymentIntent, attempt); err != nil {
			return err
		}
		return tx.operator.reconcilePaymentStateTx(ctx, tx, mode, paymentIntent, paymentAttribution{})
	})
}
