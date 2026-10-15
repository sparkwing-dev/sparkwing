package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// OpenCreditCheckout records a checkout of amountMicro that stays open until
// hold from now, and returns its id. It refuses with a
// [CreditBalanceCapError] when the team's balance, plus every checkout of the
// team still open, plus this one would pass [MaxTeamBalanceMicro], so a
// verified payment never meets a cap it cannot pass, and with a
// [PurchaseLimitError] when what the team bought over [PurchaseLimitWindow],
// plus its open checkouts, plus this one would pass its purchase limit.
// Mode and USD cents are persisted before the provider session opens.
func (t *Tenant) OpenCreditCheckout(ctx context.Context, amountMicro int64, now time.Time, hold time.Duration) (_ string, err error) {
	if amountMicro <= 0 || amountMicro%MicroCreditsPerCent != 0 || hold <= 0 {
		return "", errors.New("credits: checkout requires whole positive USD cents and a positive hold")
	}
	id, err := newCreditID("checkout")
	if err != nil {
		return "", err
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackUnlessDone(tx, &err)
	// safety: deletion takes the team row lock before it records itself, so
	// taking it here orders a checkout wholly before or after a deletion
	// request; after one, the payment would outlive the team. The ledger
	// comes first, in the order lockTeamRunRowTx documents.
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", err
	}
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return "", err
	}
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return "", err
	}
	var deleting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_deletions WHERE slug = ? AND state = ?`,
		string(t.team), TeamDeletionPending).Scan(&deleting); err != nil {
		return "", err
	}
	if deleting > 0 {
		return "", ErrTeamBeingDeleted
	}
	nowNS := now.UnixNano()
	if err := refuseAboveBalanceCapTx(ctx, tx, t.team, amountMicro, nowNS); err != nil {
		return "", err
	}
	if err := refuseAbovePurchaseLimitTx(ctx, tx, t.team, amountMicro, now); err != nil {
		return "", t.commitPurchaseRefusal(tx, id, err, now)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credit_checkouts (id, team, session_id, payment_mode, currency, amount_minor, amount_micro, opened_at, expires_at)
		VALUES (?, ?, ?, ?, 'usd', ?, ?, ?, ?)`, id, string(t.team), id, string(mode), amountMicro/MicroCreditsPerCent, amountMicro, nowNS, now.Add(hold).UnixNano()); err != nil {
		return "", fmt.Errorf("credits: record the checkout: %w", err)
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: t.team, Kind: BusinessEventCheckoutOpened, SubjectID: id,
		Attrs: map[string]any{"amount_micro": amountMicro},
	}); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// safety: a refused purchase is recorded as a business event, so the refusal
// commits rather than rolling back with the checkout it refused.
func (t *Tenant) commitPurchaseRefusal(tx *storeTx, checkoutID string, refusal error, now time.Time) error {
	var limit *PurchaseLimitError
	if !errors.As(refusal, &limit) {
		return refusal
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Kind: BusinessEventPurchaseLimitRefused, Team: t.team, SubjectID: checkoutID, At: now,
		Attrs: map[string]any{
			"trusted": limit.Trusted, "limit_cents": limit.LimitMicro / MicroCreditsPerCent,
			"purchased_cents": limit.PurchasedMicro / MicroCreditsPerCent,
			"amount_cents":    limit.AmountMicro / MicroCreditsPerCent,
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return refusal
}

func openCheckoutMicroTx(ctx context.Context, q rowQuerier, team Team, nowNS int64) (int64, error) {
	var open sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT SUM(amount_micro) FROM credit_checkouts WHERE team = ? AND paid_at IS NULL AND closed_at IS NULL AND expires_at > ?`,
		string(team), nowNS).Scan(&open)
	return open.Int64, err
}

// CheckoutSettleWindow is how long after its session expires an unpaid
// checkout keeps counting against the purchase limit. Stripe retries a
// webhook for up to three days, so a session paid just before it expired can
// be granted that late.
const CheckoutSettleWindow = 72 * time.Hour

// safety: a session paid just before expiry is granted when its webhook lands, possibly days later,
// so it keeps counting against the purchase limit until then, or a second checkout could spend the
// same room and both payments land. A session the processor reported closed can no longer be paid.
func settlingCheckoutMicroTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (int64, error) {
	var open sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT SUM(amount_micro) FROM credit_checkouts
	  WHERE team = ? AND paid_at IS NULL AND closed_at IS NULL
	    AND (expires_at > ? OR (session_id != id AND expires_at > ?))`,
		string(team), now.UnixNano(), now.Add(-CheckoutSettleWindow).UnixNano()).Scan(&open)
	return open.Int64, err
}

// AttachCreditCheckout names the payment session a checkout opened and the
// moment it expires, so the paid grant finds the checkout and an unpaid one
// stops counting against the cap when the session can no longer be paid.
func (t *Tenant) AttachCreditCheckout(ctx context.Context, id, sessionID string, expiresAt time.Time) error {
	if sessionID == "" || sessionID == id {
		return ErrInvalidInput
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return err
		}
		var prior string
		var expires int64
		var paid, closed sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT session_id,expires_at,paid_at,closed_at FROM credit_checkouts WHERE team=? AND id=?"+tx.forUpdate(), t.team, id).Scan(&prior, &expires, &paid, &closed); err != nil {
			return err
		}
		if prior == sessionID && expires == expiresAt.UnixNano() {
			return nil
		}
		if prior != id || paid.Valid || closed.Valid || !expiresAt.After(tx.now()) || expiresAt.UnixNano() > expires {
			return ErrInvalidInput
		}
		_, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET session_id=?,expires_at=? WHERE team=? AND id=?", sessionID, expiresAt.UnixNano(), t.team, id)
		return err
	})
}

// DropCreditCheckout closes a checkout whose session never opened and
// records it as [BusinessEventCheckoutFailed] at the session stage.
func (t *Tenant) DropCreditCheckout(ctx context.Context, id string) (err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	var session string
	var closed sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT session_id,closed_at FROM credit_checkouts WHERE team=? AND id=?"+tx.forUpdate(), t.team, id).Scan(&session, &closed); err != nil {
		return err
	}
	if session != id {
		return ErrInvalidInput
	}
	if closed.Valid {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET closed_at=? WHERE team=? AND id=?", tx.now().UnixNano(), t.team, id); err != nil {
		return err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: t.team, Kind: BusinessEventCheckoutFailed, SubjectID: id,
		Attrs: map[string]any{"stage": "session_open"},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// safety: runs inside the paid grant's transaction, so the checkout stops
// counting as open in the same step its amount enters the balance.
func markCreditCheckoutPaidTx(ctx context.Context, tx *storeTx, team Team, sessionID string, nowNS int64) error {
	if sessionID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE credit_checkouts SET paid_at = ? WHERE team = ? AND session_id = ? AND paid_at IS NULL`,
		nowNS, string(team), sessionID)
	return err
}
