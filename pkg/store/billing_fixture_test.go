package store

import (
	"context"
	"time"
)

// TestOnlyOpenCreditCheckout reserves a prepaid checkout before opening its provider session.
func (t *Tenant) TestOnlyOpenCreditCheckout(ctx context.Context, amountMicro int64, now time.Time, hold time.Duration) (_ string, err error) {
	var id string
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		var e error
		id, e = t.openCreditCheckoutTx(ctx, tx, amountMicro, now, hold)
		return e
	})
	return id, err
}

// TestOnlyAttachCreditCheckout binds fixture settlement facts.
func (t *Tenant) TestOnlyAttachCreditCheckout(ctx context.Context, id, sessionID string, expiresAt time.Time) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		result, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET session_id=?,expires_at=? WHERE team=? AND id=? AND session_id=id", sessionID, expiresAt.UnixNano(), t.team, id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "fixture checkout binding")
	})
}

// TestOnlySetBillingTrust commits a standing decision for fixtures without an operator session.
func (t *Tenant) TestOnlySetBillingTrust(ctx context.Context, c BillingTrustChange, now time.Time) (before, after BillingStanding, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error { before, after, err = t.setBillingTrustTx(ctx, tx, c, now); return err })
	return before, after, err
}
