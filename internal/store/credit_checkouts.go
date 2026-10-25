package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (t *Tenant) openCreditCheckoutTx(ctx context.Context, tx *storeTx, amountMicro int64, now time.Time, hold time.Duration) (string, error) {
	if amountMicro < MinCardChargeCents*MicroCreditsPerCent || amountMicro > MaxCardPaymentCents*MicroCreditsPerCent || amountMicro%MicroCreditsPerCent != 0 || hold <= 0 {
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
	if _, err := lockLedgerTx(ctx, tx, t.team); err != nil {
		return "", err
	}
	var deleting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_deletions WHERE slug = ?`,
		string(t.team)).Scan(&deleting); err != nil {
		return "", err
	}
	if deleting > 0 {
		return "", ErrTeamBeingDeleted
	}
	nowNS := now.UnixNano()
	if _, err := tx.execContext(ctx, `
		INSERT INTO credit_checkouts (id, team, session_id, payment_mode, currency, amount_minor, amount_micro, opened_at, expires_at)
		VALUES (?, ?, ?, ?, 'usd', ?, ?, ?, ?)`, id, string(t.team), id, string(mode), amountMicro/MicroCreditsPerCent, amountMicro, nowNS, now.Add(hold).UnixNano()); err != nil {
		return "", fmt.Errorf("credits: record the checkout: %w", err)
	}
	if err := tx.operator.recordAudit(ctx, tx, auditContext{At: now, Team: t.team, Actor: auditActor{system: auditController}}, auditCheckout{CheckoutID: id, Outcome: businessEventCheckoutOpened, AmountMicro: amountMicro}); err != nil {
		return "", err
	}
	return id, nil
}
