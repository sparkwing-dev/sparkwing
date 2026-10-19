package store

import (
	"context"
	"fmt"
	"time"
)

// DueCardRefunds lists due refunds across the deployment.
func (o *Operator) DueCardRefunds(ctx context.Context, now time.Time) (_ []CardRefundWork, err error) {
	rows, err := o.s.query(ctx, `SELECT team, payment_intent, reason, attempts FROM card_refunds
	    WHERE status = ? AND next_attempt_at <= ? ORDER BY created_at`, CardRefundDue, now.UnixNano())
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
	if !ok {
		return fmt.Errorf("%w: refund key %q", ErrInvalidInput, key)
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		status := status
		recorded, err := refundOutcomeTx(ctx, tx, paymentIntent, refundID)
		if err != nil {
			return err
		}
		if recorded == CardRefundSucceeded || status == CardRefundPending && recorded != "" {
			status = recorded
		}
		_, err = applyCardRefundTx(ctx, tx, team, paymentIntent, refundID, status,
			`attempts = ? AND status = 'due'`, []any{attempt}, now)
		return err
	})
}
