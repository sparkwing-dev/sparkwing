package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
func (o *Operator) RecordAttemptIntent(ctx context.Context, attemptID, paymentIntent, customer string, now time.Time) error {
	if attemptID == "" || paymentIntent == "" || customer == "" {
		return fmt.Errorf("%w: a collection binding names its attempt, payment and provider-request customer", ErrInvalidInput)
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return err
		}
		return recordAttemptIntentTx(ctx, tx, attemptID, paymentIntent, customer, now)
	})
}

func recordAttemptIntentTx(ctx context.Context, tx *storeTx, attemptID, paymentIntent, customer string, now time.Time) error {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	var team Team
	var charge, bound string
	if err := tx.QueryRowContext(ctx, `SELECT a.team,a.charge_id,a.payment_intent
FROM card_attempts a JOIN card_charges c ON c.id=a.charge_id AND c.team=a.team
WHERE a.id=? AND a.status=?`+tx.forUpdate(), attemptID, CardAttemptLive).Scan(&team, &charge, &bound); err != nil {
		return err
	}
	if bound != "" && bound != paymentIntent {
		return authorityError(apierr.Conflict, "collection attempt names another payment")
	}
	var checkouts int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credit_checkouts WHERE payment_mode=? AND payment_id=?", mode, paymentIntent).Scan(&checkouts); err != nil {
		return err
	}
	if checkouts != 0 {
		return authorityError(apierr.Conflict, "payment already belongs to a checkout")
	}
	var associated int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND team=? AND kind='collection' AND currency='usd' AND attempt_id=? AND charge_id=? AND customer_id=?`, mode, paymentIntent, team, attemptID, charge, customer).Scan(&associated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if associated == 0 {
		if bound != "" {
			return authorityError(apierr.Conflict, "collection has no matching provider association")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id,currency,attempt_id,charge_id) VALUES (?,?,'collection',?,?,'usd',?,?)`, mode, paymentIntent, team, customer, attemptID, charge); err != nil {
			return err
		}
	}
	if bound == paymentIntent {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE card_attempts SET payment_intent=?,updated_at=? WHERE team=? AND id=? AND status=? AND payment_intent=''`, paymentIntent, now.UnixNano(), team, attemptID, CardAttemptLive)
	if err != nil {
		return err
	}
	return requireOneRow(result, "collection payment binding")
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
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return err
		}
		recorded, err := recordedRefundOutcomeTx(ctx, tx, team, paymentIntent, refundID)
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
