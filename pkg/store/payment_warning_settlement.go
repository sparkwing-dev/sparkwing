package store

import (
	"context"
	"fmt"
	"time"
)

// safety: an actionable warning blocks its card everywhere, and on a payment this deployment settled it holds the
// team. Liability-shifted payments keep their credit frozen by that hold; unprotected payments reverse their
// credit and queue a full refund unless recorded refunds already returned it.
func recordPaymentWarningTx(ctx context.Context, tx *storeTx, w PaymentWarning, now time.Time) (Team, error) {
	if w.WarningID == "" || w.PaymentIntent == "" {
		return "", fmt.Errorf("%w: a warning names itself and its payment", ErrInvalidInput)
	}
	actionable := 0
	if w.Actionable {
		actionable = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO payment_warnings (warning_id, payment_intent, fingerprint, actionable,
	    created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (warning_id) DO NOTHING`,
		w.WarningID, w.PaymentIntent, w.Fingerprint, actionable, now.UnixNano()); err != nil {
		return "", err
	}
	var payment, fingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT payment_intent,fingerprint FROM payment_warnings WHERE warning_id=?`, w.WarningID).Scan(&payment, &fingerprint); err != nil {
		return "", err
	}
	if payment != w.PaymentIntent || fingerprint != w.Fingerprint {
		return "", fmt.Errorf("%w: warning identity names another payment or card", ErrCreditGrantConflict)
	}
	if !w.Actionable {
		return "", nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE payment_warnings SET actionable=1 WHERE warning_id=?", w.WarningID); err != nil {
		return "", err
	}
	if err := forgetCardTx(ctx, tx, fingerprint, CardRefusedWarning, now); err != nil {
		return "", err
	}
	team, err := paymentTeamTx(ctx, tx, "", payment)
	if err != nil || team == "" {
		return "", err
	}
	if err := holdForWarningTx(ctx, tx, team, w.WarningID, payment, now); err != nil {
		return "", err
	}
	grant, funded, err := paidGrantForPaymentTx(ctx, tx, payment)
	if err != nil {
		return "", err
	}
	// An in-flight payment has no credit to reverse; its paid event verifies protection before choosing a frozen
	// grant or a refund. A payment already refused was queued for refund when it was.
	if !funded {
		return team, nil
	}
	var shifted int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(liability_shift,0) FROM credit_checkouts WHERE team = ? AND payment_id = ? AND paid_at IS NOT NULL`, string(team), payment).Scan(&shifted); err != nil {
		return "", err
	}
	if shifted == 1 {
		return team, nil
	}
	if _, err := reversePaymentTx(ctx, tx, payment, "fraud_warning:"+w.WarningID, "payment_provider", 0); err != nil {
		return "", err
	}
	if err := queueCardRefundTx(ctx, tx, team, payment, grant.AmountMicro/MicroCreditsPerCent, CardRefusedWarning, now); err != nil {
		return "", err
	}
	return team, nil
}
