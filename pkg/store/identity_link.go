package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Identity is one sign-in method attached to an account: an identity
// provider's account, keyed by the provider's stable subject.
type Identity struct {
	Provider string
	Subject  string
	// Email is the address the provider asserted at the identity's latest
	// sign-in or link.
	Email string
	// Linked is true for an identity the account holder attached from
	// settings. A linked identity never sets the account's email.
	Linked    bool
	CreatedAt time.Time
}

// Identity linking errors. Callers map these onto status codes.
var (
	ErrIdentityLinkedElsewhere = errors.New("store: that sign-in belongs to another account")
	ErrIdentityAlreadyLinked   = errors.New("store: that sign-in is already linked to this account")
	ErrProviderAlreadyLinked   = errors.New("store: this account already has a sign-in from that provider")
	ErrLastSignInMethod        = errors.New("store: an account keeps at least one sign-in method")
)

// safety: an older binary reads a linked identity as one that signed in, and
// so lets its sign-ins set the account's email, until this binary runs again.
var identityLinkedCols = map[string]string{"linked": "INTEGER NOT NULL DEFAULT 0"}

// identity_unlinks remembers which account let go of which provider account,
// so the email rule does not attach it straight back.
const identityLinkTablesSQLite = `
CREATE TABLE IF NOT EXISTS identity_link_states (
    nonce      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS identity_unlinks (
    provider    TEXT NOT NULL,
    subject     TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    unlinked_at INTEGER NOT NULL,
    PRIMARY KEY (provider, subject, account_id)
);
CREATE INDEX IF NOT EXISTS idx_identity_unlinks_account ON identity_unlinks(account_id)
`

var identityLinkTablesPostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(identityLinkTablesSQLite)

func applyIdentityLinkMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "identities", identityLinkedCols); err != nil {
		return err
	}
	return execStatements(ctx, tx, identityLinkTablesSQLite)
}

func applyIdentityLinkMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "identities", identityLinkedCols); err != nil {
		return err
	}
	return execStatements(ctx, tx, identityLinkTablesPostgres)
}

// AccountIdentities lists the sign-in methods attached to accountID, oldest
// first.
func (s *Store) AccountIdentities(ctx context.Context, accountID string) (_ []Identity, err error) {
	rows, err := s.query(ctx, `SELECT provider, subject, email, linked, created_at FROM identities
		WHERE account_id = ? ORDER BY created_at, provider`, accountID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []Identity
	for rows.Next() {
		var id Identity
		var linked int
		var created int64
		if err := rows.Scan(&id.Provider, &id.Subject, &id.Email, &linked, &created); err != nil {
			return nil, err
		}
		id.Linked = linked == 1
		id.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, id)
	}
	return out, rows.Err()
}

// LinkIdentity attaches the provider account p describes to accountID as a
// further sign-in method. The provider's address plays no part: the caller
// has proven that this account's holder controls the provider account.
//
// It refuses, changing nothing, a provider account already attached to any
// account ([ErrIdentityLinkedElsewhere], or [ErrIdentityAlreadyLinked] when
// that account is accountID) and a second provider account from a provider
// accountID already signs in with ([ErrProviderAlreadyLinked]). The
// account's email stays what it was.
func (s *Store) LinkIdentity(ctx context.Context, accountID string, p SignInProfile, now time.Time) (Identity, error) {
	if p.Provider == "" || p.Subject == "" {
		return Identity{}, fmt.Errorf("%w: a link names no provider or subject", ErrInvalidInput)
	}
	p.Email = NormalizeEmail(p.Email)
	id, err := s.linkIdentityOnce(ctx, accountID, p, now)
	// safety: two links of one provider account race on the identities key;
	// the loser reads the winner's row and answers as if it had come second.
	if isUniqueViolation(err) {
		id, err = s.linkIdentityOnce(ctx, accountID, p, now)
	}
	return id, err
}

func (s *Store) linkIdentityOnce(ctx context.Context, accountID string, p SignInProfile, now time.Time) (Identity, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Identity{}, err
	}
	defer rollbackOrLog(tx)
	if _, err := accountTx(ctx, tx, accountID); err != nil {
		return Identity{}, err
	}
	var holder string
	err = tx.QueryRowContext(ctx,
		`SELECT account_id FROM identities WHERE provider = ? AND subject = ?`, p.Provider, p.Subject).Scan(&holder)
	switch {
	case err == nil && holder == accountID:
		return Identity{}, ErrIdentityAlreadyLinked
	case err == nil:
		return Identity{}, ErrIdentityLinkedElsewhere
	case !errors.Is(err, sql.ErrNoRows):
		return Identity{}, err
	}
	var sameProvider int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM identities WHERE account_id = ? AND provider = ?`, accountID, p.Provider).Scan(&sameProvider); err != nil {
		return Identity{}, err
	}
	if sameProvider > 0 {
		return Identity{}, ErrProviderAlreadyLinked
	}
	at := now.UTC().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO identities (provider, subject, account_id, email, email_verified, created_at, linked)
		VALUES (?, ?, ?, ?, ?, ?, 1)`,
		p.Provider, p.Subject, accountID, p.Email, boolInt(p.EmailVerified), at); err != nil {
		return Identity{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM identity_unlinks WHERE provider = ? AND subject = ? AND account_id = ?`,
		p.Provider, p.Subject, accountID); err != nil {
		return Identity{}, err
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, err
	}
	return Identity{
		Provider: p.Provider, Subject: p.Subject, Email: p.Email, Linked: true,
		CreatedAt: time.Unix(at, 0).UTC(),
	}, nil
}

// UnlinkIdentity detaches accountID's sign-in from provider and returns it.
// An account keeps at least one sign-in method ([ErrLastSignInMethod]), and
// a provider it has no sign-in from is [ErrNotFound]. The account's email
// stays what it was.
//
// The provider account then signs in as a stranger: the email rule no longer
// attaches it to accountID, even on the account's own verified address. Every
// session of accountID other than keepSession, a raw session id, ends,
// because one of them may have been opened with the sign-in just removed.
func (s *Store) UnlinkIdentity(ctx context.Context, accountID, provider, keepSession string, now time.Time) (Identity, int64, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Identity{}, 0, err
	}
	defer rollbackOrLog(tx)
	var id Identity
	var linked int
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT provider, subject, email, linked, created_at FROM identities
		WHERE account_id = ? AND provider = ? ORDER BY created_at LIMIT 1`, accountID, provider).
		Scan(&id.Provider, &id.Subject, &id.Email, &linked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, 0, ErrNotFound
	}
	if err != nil {
		return Identity{}, 0, err
	}
	id.Linked, id.CreatedAt = linked == 1, time.Unix(created, 0).UTC()
	var methods int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM identities WHERE account_id = ?`, accountID).Scan(&methods); err != nil {
		return Identity{}, 0, err
	}
	if methods <= 1 {
		return Identity{}, 0, ErrLastSignInMethod
	}
	at := now.UTC().Unix()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM identities WHERE provider = ? AND subject = ? AND account_id = ?`, []any{id.Provider, id.Subject, accountID}},
		{`DELETE FROM identity_unlinks WHERE provider = ? AND subject = ? AND account_id = ?`, []any{id.Provider, id.Subject, accountID}},
		{`INSERT INTO identity_unlinks (provider, subject, account_id, unlinked_at) VALUES (?, ?, ?, ?)`,
			[]any{id.Provider, id.Subject, accountID, at}},
	} {
		if _, err := tx.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return Identity{}, 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id = ? AND hash <> ?`,
		accountID, sessionDigest(keepSession))
	if err != nil {
		return Identity{}, 0, err
	}
	ended, err := res.RowsAffected()
	if err != nil {
		return Identity{}, 0, err
	}
	return id, ended, tx.Commit()
}

// ConsumeIdentityLinkState records that the link flow named by nonce
// finished, and reports false when one already did. The record lives until
// the state expires, so every replica and a restarted controller refuse a
// state that was used once.
func (s *Store) ConsumeIdentityLinkState(ctx context.Context, nonce string, expires, now time.Time) (bool, error) {
	return s.consumeFlowNonce(ctx, "identity_link_states", nonce, expires, now)
}

// safety: table is one of this package's own flow-state tables, never input.
func (s *Store) consumeFlowNonce(ctx context.Context, table, nonce string, expires, now time.Time) (bool, error) {
	if nonce == "" {
		return false, nil
	}
	if _, err := s.exec(ctx, `DELETE FROM `+table+` WHERE expires_at <= ?`, now.Unix()); err != nil {
		return false, err
	}
	_, err := s.exec(ctx, `INSERT INTO `+table+` (nonce, expires_at) VALUES (?, ?)`, nonce, expires.Unix())
	if isUniqueViolation(err) {
		return false, nil
	}
	return err == nil, err
}

// IdentityLinkStateKey is the key a controller signs link-flow state with.
// It derives from the deployment's session key, so every replica agrees on
// it and rotating that key voids flows in progress along with sessions.
func (s *Store) IdentityLinkStateKey() ([]byte, error) {
	key, err := s.csrfSigningKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("sparkwing identity link state v1"))
	return mac.Sum(nil), nil
}
