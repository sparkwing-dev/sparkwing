package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// OpenCreditCheckout reserves a prepaid checkout before opening its provider session.
func (t *Tenant) OpenCreditCheckout(ctx context.Context, amountMicro int64, now time.Time, hold time.Duration) (_ string, err error) {
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

func (t *Tenant) openCreditCheckoutTx(ctx context.Context, tx *storeTx, amountMicro int64, now time.Time, hold time.Duration) (string, error) {
	if amountMicro < MinCardChargeCents*MicroCreditsPerCent || amountMicro > MaxCreditGrantMicro || amountMicro%MicroCreditsPerCent != 0 || hold <= 0 {
		return "", errors.New("credits: checkout requires whole positive USD cents and a positive hold")
	}
	id, err := newCreditID("checkout")
	if err != nil {
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
	return id, nil
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
