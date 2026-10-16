package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
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

// Identity linking and sign-in errors. Callers map these onto status codes.
var (
	ErrIdentityLinkedElsewhere = errors.New("store: that sign-in belongs to another account")
	ErrIdentityAlreadyLinked   = errors.New("store: that sign-in is already linked to this account")
	ErrProviderAlreadyLinked   = errors.New("store: this account already has a sign-in from that provider")
	ErrLastSignInMethod        = errors.New("store: an account keeps at least one sign-in method")
	ErrIdentityUnlinked        = errors.New("store: sign-in method is no longer linked to this account")
)

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
		id.CreatedAt = time.Unix(0, created).UTC()
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
	at := now.UTC().UnixNano()
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
		CreatedAt: time.Unix(0, at).UTC(),
	}, nil
}

func unlinkAccountIdentityTx(ctx context.Context, tx *storeTx, accountID, provider, keepHash string, now time.Time) (int64, error) {
	if err := lockAccountTx(ctx, tx, accountID); err != nil {
		return 0, err
	}
	var subject string
	err := tx.QueryRowContext(ctx, `SELECT subject FROM identities
		WHERE account_id = ? AND provider = ? ORDER BY created_at LIMIT 1`, accountID, provider).
		Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	var methods int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM identities WHERE account_id = ?`, accountID).Scan(&methods); err != nil {
		return 0, err
	}
	if methods <= 1 {
		return 0, ErrLastSignInMethod
	}
	at := now.UTC().UnixNano()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM identities WHERE provider = ? AND subject = ? AND account_id = ?`, []any{provider, subject, accountID}},
		{`DELETE FROM identity_unlinks WHERE provider = ? AND subject = ? AND account_id = ?`, []any{provider, subject, accountID}},
		{
			`INSERT INTO identity_unlinks (provider, subject, account_id, unlinked_at) VALUES (?, ?, ?, ?)`,
			[]any{provider, subject, accountID, at},
		},
	} {
		if _, err := tx.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id = ? AND hash <> ?`,
		accountID, keepHash)
	if err != nil {
		return 0, err
	}
	ended, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return ended, nil
}

func lockAccountTx(ctx context.Context, tx *storeTx, accountID string) error {
	if tx.dialect == DialectSQLite {
		res, err := tx.ExecContext(ctx, `UPDATE accounts SET id = id WHERE id = ?`, accountID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id = ? FOR UPDATE`, accountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ConsumeIdentityLinkState records that the link flow named by nonce
// finished, and reports false when one already did. The record lives until
// the state expires, so every replica and a restarted controller refuse a
// state that was used once.
func (s *Store) ConsumeIdentityLinkState(ctx context.Context, nonce string, expires, now time.Time) (bool, error) {
	return s.consumeFlowNonce(ctx, "identity_link", nonce, expires)
}

func (s *Store) consumeFlowNonce(ctx context.Context, kind, nonce string, expires time.Time) (bool, error) {
	if nonce == "" {
		return false, nil
	}
	used := false
	err := s.inTx(ctx, func(tx *storeTx) error {
		if !expires.After(tx.now()) {
			return nil
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO flow_nonces(kind,nonce,expires_at) VALUES (?,?,?) ON CONFLICT(kind,nonce) DO NOTHING`, kind, nonce, expires.UnixNano())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		used = n == 1
		return err
	})
	return err == nil && used, err
}

// IdentityLinkStateKey is the key a controller signs link-flow state with.
// It derives from the deployment's session key, so every replica agrees on
// it and rotating that key voids flows in progress along with sessions.
func (s *Store) IdentityLinkStateKey(ctx context.Context) ([]byte, error) {
	key, err := s.csrfSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("sparkwing identity link state v1"))
	return mac.Sum(nil), nil
}
