package store

import (
	"cmp"
	"context"
	"fmt"
	"time"
)

// DueCardCharges opens deployment charges and starts due collection attempts.
func (o *Operator) DueCardCharges(ctx context.Context, now time.Time) (_ []CardChargeWork, _ []BudgetAlert, err error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return nil, nil, err
	}
	nowNS := now.UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'expired', updated_at = ?
	    WHERE status = ? AND kind = ? AND created_at < ?`, CardAttemptFailed, nowNS, CardAttemptLive,
		CardAttemptRecovery, now.Add(-recoveryAttemptLife).UnixNano()); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'key_expired', updated_at = ?
	    WHERE status = ? AND kind = ? AND payment_intent = '' AND created_at < ?`, CardAttemptFailed, nowNS,
		CardAttemptLive, CardAttemptOffSession, now.Add(-cardKeyLife).UnixNano()); err != nil {
		return nil, nil, err
	}
	teams, err := billedTeamsTx(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	var alerts []BudgetAlert
	for _, team := range teams {
		if team.carded {
			if err := decideCardChargeTx(ctx, tx, team.name, now); err != nil {
				return nil, nil, err
			}
		}
		alert, err := budgetAlertTx(ctx, tx, team.name, now)
		if err != nil {
			return nil, nil, err
		}
		if alert != nil {
			alerts = append(alerts, *alert)
		}
	}
	work, err := startDueAttemptsTx(ctx, tx, now)
	if err != nil {
		return nil, nil, err
	}
	return work, alerts, tx.Commit()
}

// RecordAttemptIntent binds a live attempt to its provider payment identity.
func (o *Operator) RecordAttemptIntent(ctx context.Context, attemptID, paymentIntent string, now time.Time) error {
	if attemptID == "" || paymentIntent == "" {
		return fmt.Errorf("%w: an attempt's intent names both", ErrInvalidInput)
	}
	res, err := o.s.exec(ctx, `UPDATE card_attempts SET payment_intent = ?, updated_at = ?
	    WHERE id = ? AND status = ? AND (payment_intent = '' OR payment_intent = ?)`,
		paymentIntent, now.UnixNano(), attemptID, CardAttemptLive, paymentIntent)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmp.Or(err, fmt.Errorf("%w: attempt %q is not live or has another payment", ErrInvalidInput, attemptID))
	}
	return nil
}

// SettleCardPayment settles a provider payment against its persisted card attempt.
func (o *Operator) SettleCardPayment(ctx context.Context, p CardPayment, now time.Time) (created bool, warned *WarnedCardPayment, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		created, warned, err = settleCardPaymentTx(ctx, tx, p, now)
		return err
	})
	if err != nil {
		return false, nil, err
	}
	return created, warned, err
}

// RecordPaymentWarning records provider fraud evidence and holds its persisted payer.
func (o *Operator) RecordPaymentWarning(ctx context.Context, w PaymentWarning, now time.Time) (team Team, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		team, err = recordPaymentWarningTx(ctx, tx, w, now)
		return err
	})
	if err != nil {
		return "", err
	}
	return team, nil
}

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
		_, err := applyCardRefundTx(ctx, tx, team, paymentIntent, refundID, status,
			`attempts = ? AND status = 'due'`, []any{attempt}, now)
		return err
	})
}

// ReportCardRefund applies a provider refund outcome to its current queued attempt.
func (o *Operator) ReportCardRefund(ctx context.Context, key, refundID, status string, now time.Time) (changed bool, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		changed, err = reportCardRefundTx(ctx, tx, key, refundID, status, now)
		return err
	})
	if err != nil {
		return false, err
	}
	return changed, err
}

// FailCardAttempt records a failed collection attempt and its payer hold.
func (o *Operator) FailCardAttempt(ctx context.Context, attemptID, paymentIntent, declineCode string, now time.Time) (team Team, first bool, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		team, first, err = failCardAttemptTx(ctx, tx, attemptID, paymentIntent, declineCode, now)
		return err
	})
	if err != nil {
		return "", false, err
	}
	return team, first, err
}

// DropCardAttempt closes a live collection attempt that never reached the provider.
func (o *Operator) DropCardAttempt(ctx context.Context, attemptID string, now time.Time) error {
	_, err := o.s.exec(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'not_opened', updated_at = ?
	    WHERE id = ? AND status = ?`, CardAttemptFailed, now.UnixNano(), attemptID, CardAttemptLive)
	return err
}
