package store

import (
	"context"
	"time"
)

// TestOnlyOpenCreditCheckout reserves a prepaid checkout before opening its provider session.
func (t *Tenant) TestOnlyOpenCreditCheckout(ctx context.Context, amountMicro int64, now time.Time, hold time.Duration) (_ string, err error) {
	var id string
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return err
		}
		var e error
		id, e = t.openCreditCheckoutTx(ctx, tx, amountMicro, now, hold)
		return e
	})
	return id, err
}

// TestOnlyAttachCreditCheckout binds fixture settlement facts.
func (t *Tenant) TestOnlyAttachCreditCheckout(ctx context.Context, id, sessionID string, expiresAt time.Time) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET session_id=?,expires_at=? WHERE team=? AND id=? AND session_id=id", sessionID, expiresAt.UnixNano(), t.team, id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "fixture checkout binding")
	})
}

// TestOnlySetSpendBudget sets the owner's budget, which stops Cloud usage when the team's 30-day spend reaches it.
func (t *Tenant) TestOnlySetSpendBudget(ctx context.Context, cents int64) error {
	return t.s.inTx(ctx, func(tx *storeTx) error { return setSpendBudgetTx(ctx, tx, t.team, cents) })
}

// TestOnlyStartRecoveryAttempt opens a "pay now" attempt on t's open charge, for a Checkout the owner pays by hand.
func (t *Tenant) TestOnlyStartRecoveryAttempt(ctx context.Context, now time.Time) (_ CardChargeWork, err error) {
	var w CardChargeWork
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return err
		}
		var e error
		w, e = t.startRecoveryAttemptTx(ctx, tx, now)
		return e
	})
	return w, err
}
