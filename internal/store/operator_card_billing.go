package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// DueCardRefunds lists due refunds across the deployment.
func (o *Operator) DueCardRefunds(ctx context.Context) (_ []CardRefundWork, err error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.queryContext(ctx, `SELECT team,payment_intent,reason FROM card_refunds WHERE payment_mode=? AND status=? ORDER BY created_at`, mode, cardRefundDue)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CardRefundWork
	for rows.Next() {
		var w CardRefundWork
		if err := rows.Scan(&w.Team, &w.PaymentIntent, &w.Reason); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkCardRefundMade binds the provider refund ID to the payment's queued refund.
// Only verified callbacks establish the refund's amount and outcome.
func (o *Operator) MarkCardRefundMade(ctx context.Context, team Team, paymentIntent, refundID string) error {
	if paymentIntent == "" || refundID == "" {
		return fmt.Errorf("%w: a refund names its payment and provider refund", ErrInvalidInput)
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		// Existing refund obligations use the retained payer tombstone.
		mode, err := paymentModeTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := lockFinancialTeamsTx(ctx, tx, team); err != nil {
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
		if _, err := tx.execContext(ctx, `UPDATE card_refunds SET refund_id=?, status='pending' WHERE payment_mode=? AND team=? AND payment_intent=? AND status='due'`, refundID, mode, team, paymentIntent); err != nil {
			return err
		}
		return tx.operator.reconcilePaymentStateTx(ctx, tx, mode, paymentIntent, paymentAttribution{})
	})
}

// MarkCardRefundFailed closes a queued refund the checkout service refused, or never answered, before Stripe made
// it. Like a refund Stripe failed, it is listed for the operator, announced to the team's owners and never retried.
func (o *Operator) MarkCardRefundFailed(ctx context.Context, team Team, paymentIntent string) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		mode, err := paymentModeTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := lockFinancialTeamsTx(ctx, tx, team); err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE card_refunds SET status='failed' WHERE payment_mode=? AND team=? AND payment_intent=? AND status='due'`, mode, team, paymentIntent)
		return err
	})
}
