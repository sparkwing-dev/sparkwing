package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// safety: an open checkout is money a team may pay at any moment, and the
// grant that follows a verified payment is never refused, so the balance cap
// is held when a checkout opens by counting every checkout still open. A row
// stops counting once its payment is granted or its session expires.
const creditCheckoutsTableSQLite = `CREATE TABLE IF NOT EXISTS credit_checkouts (
    id           TEXT PRIMARY KEY,
    team         TEXT NOT NULL,
    session_id   TEXT NOT NULL DEFAULT '',
    amount_micro INTEGER NOT NULL,
    opened_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    paid_at      INTEGER
);
CREATE INDEX IF NOT EXISTS idx_credit_checkouts_team_expires ON credit_checkouts(team, expires_at);
CREATE INDEX IF NOT EXISTS idx_credit_checkouts_session ON credit_checkouts(session_id);`

var creditCheckoutsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(creditCheckoutsTableSQLite)

// safety: a frozen team's cloud usage is held while a payment dispute is
// open; zero is not frozen, and the reason says which dispute froze it.
var teamsCreditFreezeCols = map[string]string{
	"credit_frozen_at":     "INTEGER NOT NULL DEFAULT 0",
	"credit_frozen_reason": "TEXT NOT NULL DEFAULT ''",
}

func applyCreditCheckoutMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(creditCheckoutsTableSQLite) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return ensureColumnsSQLite(ctx, tx, "teams", teamsCreditFreezeCols)
}

func applyCreditCheckoutMigrationPostgres(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(creditCheckoutsTablePostgres) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return addColumnsTx(ctx, tx, "teams", teamsCreditFreezeCols)
}

// OpenCreditCheckout records a checkout of amountMicro that stays open until
// hold from now, and returns its id. It refuses with a
// [CreditBalanceCapError] when the team's balance, plus every checkout of the
// team still open, plus this one would pass [MaxTeamBalanceMicro], so a
// verified payment never meets a cap it cannot pass. Checkouts already
// expired are removed.
func (t *Tenant) OpenCreditCheckout(ctx context.Context, amountMicro int64, now time.Time, hold time.Duration) (_ string, err error) {
	if amountMicro <= 0 {
		return "", errors.New("credits: a checkout amount must be positive")
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
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", err
	}
	nowNS := now.UnixNano()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM credit_checkouts WHERE team = ? AND expires_at <= ?`, string(t.team), nowNS); err != nil {
		return "", err
	}
	if err := refuseAboveBalanceCapTx(ctx, tx, t.team, amountMicro, nowNS); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credit_checkouts (id, team, amount_micro, opened_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, id, string(t.team), amountMicro, nowNS, now.Add(hold).UnixNano()); err != nil {
		return "", fmt.Errorf("credits: record the checkout: %w", err)
	}
	return id, tx.Commit()
}

// openCheckoutMicroTx is what the team's checkouts still open may add.
func openCheckoutMicroTx(ctx context.Context, tx *storeTx, team Team, nowNS int64) (int64, error) {
	var open sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT SUM(amount_micro) FROM credit_checkouts WHERE team = ? AND paid_at IS NULL AND expires_at > ?`,
		string(team), nowNS).Scan(&open)
	return open.Int64, err
}

// AttachCreditCheckout names the payment session a checkout opened and the
// moment it expires, so the paid grant finds the checkout and an unpaid one
// stops counting against the cap when the session can no longer be paid.
func (t *Tenant) AttachCreditCheckout(ctx context.Context, id, sessionID string, expiresAt time.Time) error {
	_, err := t.s.exec(ctx,
		`UPDATE credit_checkouts SET session_id = ?, expires_at = ? WHERE team = ? AND id = ?`,
		sessionID, expiresAt.UnixNano(), string(t.team), id)
	return err
}

// DropCreditCheckout removes a checkout whose session never opened.
func (t *Tenant) DropCreditCheckout(ctx context.Context, id string) error {
	_, err := t.s.exec(ctx, `DELETE FROM credit_checkouts WHERE team = ? AND id = ?`, string(t.team), id)
	return err
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
