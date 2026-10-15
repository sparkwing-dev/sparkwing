package store

import (
	"context"
	"fmt"
	"time"
)

func recordPaymentWarningTx(ctx context.Context, tx *storeTx, w PaymentWarning, now time.Time) (Team, error) {
	if w.WarningID == "" || w.PaymentIntent == "" {
		return "", fmt.Errorf("%w: a warning names itself and its payment", ErrInvalidInput)
	}
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", err
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
	team, err := paymentWarningTeamTx(ctx, tx, payment)
	if err != nil || team == "" {
		return "", err
	}
	if err := holdForWarningTx(ctx, tx, team, w.WarningID, payment, now); err != nil {
		return "", err
	}
	return team, nil
}
