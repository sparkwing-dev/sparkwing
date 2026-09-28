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

const checkoutCloseWindow = 7 * 24 * time.Hour

var creditCheckoutCloseCols = map[string]string{"closed_at": "INTEGER"}

var creditCheckoutsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(creditCheckoutsTableSQLite)

// safety: a freeze is one row per dispute, so one dispute's outcome never
// releases another's hold; a team is frozen while any row is unreleased. The
// dispute id is the key and the row names the payment it disputes, so a
// dispute is bound to one payment and one team.
const creditFreezesTableSQLite = `CREATE TABLE IF NOT EXISTS credit_freezes (
    dispute_id  TEXT PRIMARY KEY,
    team        TEXT NOT NULL,
    payment_id  TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    released_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_credit_freezes_team ON credit_freezes(team, released_at);`

var creditFreezesTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(creditFreezesTableSQLite)

func applyCreditCheckoutMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(creditCheckoutsTableSQLite + "\n" + creditFreezesTableSQLite) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func applyCreditCheckoutMigrationPostgres(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(creditCheckoutsTablePostgres + "\n" + creditFreezesTablePostgres) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
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
	// safety: deletion takes the team row lock before it records itself, so
	// taking it first here orders a checkout wholly before or after a
	// deletion request; after one, the payment would outlive the team.
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
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", err
	}
	nowNS := now.UnixNano()
	// safety: an unpaid checkout outlives its hold until the processor reports
	// how it ended, so the close finds it; a week bounds a report that never comes.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM credit_checkouts WHERE team = ? AND expires_at <= ?
		   AND (paid_at IS NOT NULL OR closed_at IS NOT NULL OR expires_at <= ?)`,
		string(t.team), nowNS, now.Add(-checkoutCloseWindow).UnixNano()); err != nil {
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
	if err := RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: t.team, Kind: BusinessEventCheckoutOpened, SubjectID: id,
		Attrs: map[string]any{"amount_micro": amountMicro},
	}); err != nil {
		return "", err
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

// DropCreditCheckout removes a checkout whose session never opened and
// records it as [BusinessEventCheckoutFailed] at the session stage.
func (t *Tenant) DropCreditCheckout(ctx context.Context, id string) (err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	res, err := tx.ExecContext(ctx, `DELETE FROM credit_checkouts WHERE team = ? AND id = ?`, string(t.team), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, tx.Commit())
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: t.team, Kind: BusinessEventCheckoutFailed, SubjectID: id,
		Attrs: map[string]any{"stage": "session_open"},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrCheckoutNotFound is returned when a team holds no checkout for the
// payment session a report names.
var ErrCheckoutNotFound = errors.New("credits: the team holds no checkout for this session")

// CloseCreditCheckout moves t's checkout for sessionID from open and unpaid
// to closed with outcome, [BusinessEventCheckoutFailed] or
// [BusinessEventCheckoutExpired], records that event and stops the checkout
// counting against the balance cap. It reports whether it closed the
// checkout: a paid or already closed one is left as it is and records
// nothing. A session t holds no checkout for is [ErrCheckoutNotFound].
func (t *Tenant) CloseCreditCheckout(
	ctx context.Context, sessionID, outcome, actor string, now time.Time,
) (_ bool, err error) {
	if outcome != BusinessEventCheckoutFailed && outcome != BusinessEventCheckoutExpired {
		return false, fmt.Errorf("%w: checkout outcome %q", ErrInvalidInput, outcome)
	}
	if sessionID = strings.TrimSpace(sessionID); sessionID == "" {
		return false, fmt.Errorf("%w: a closed checkout names its session", ErrInvalidInput)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	var id string
	var paid, closed sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT id, paid_at, closed_at FROM credit_checkouts WHERE team = ? AND session_id = ?`+tx.forUpdate(),
		string(t.team), sessionID).Scan(&id, &paid, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrCheckoutNotFound
	}
	if err != nil {
		return false, err
	}
	if paid.Valid || closed.Valid {
		return false, tx.Commit()
	}
	nowNS := now.UnixNano()
	if _, err := tx.ExecContext(ctx, `
		UPDATE credit_checkouts SET closed_at = ?, expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END
		 WHERE team = ? AND id = ?`, nowNS, nowNS, nowNS, string(t.team), id); err != nil {
		return false, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: t.team, Kind: outcome, SubjectID: id, Actor: actor,
		Attrs: map[string]any{"session_id": sessionID},
	}); err != nil {
		return false, err
	}
	return true, tx.Commit()
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
