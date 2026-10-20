package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
func (o *Operator) AccountIdentities(ctx context.Context, accountID string) (_ []Identity, err error) {
	rows, err := o.s.query(ctx, `SELECT provider, subject, email, linked, created_at FROM identities
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

// IdentityChangeSignInWindow bounds how old a sign-in may be when it adds or removes a sign-in method.
const IdentityChangeSignInWindow = 10 * time.Minute

// ConsumeIdentityLink records the link nonce once, bound to its direct session, account, provider and expiry.
func (o *Operator) ConsumeIdentityLink(ctx context.Context, claim capability.IdentityLink) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := identityLinkAuthorityTx(ctx, tx, claim); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO flow_nonces(kind,nonce,expires_at,binding) VALUES ('identity_link',?,?,?) ON CONFLICT(kind,nonce) DO NOTHING`, claim.Nonce, claim.ExpiresAt.UnixNano(), identityLinkBinding(claim))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errors.Join(identityLinkInvalid(), err)
		}
		return nil
	})
}

// LinkAccountIdentity attaches the verified provider profile to the claim's account and completes its nonce.
// The account's email is unchanged; identity uniqueness refusals are the Err* sentinels.
func (o *Operator) LinkAccountIdentity(ctx context.Context, claim capability.IdentityLink, p SignInProfile) (Identity, error) {
	var out Identity
	attempt := func() error {
		return o.s.inTx(ctx, func(tx *storeTx) error {
			var err error
			out, err = linkAccountIdentityTx(ctx, tx, claim, p)
			return err
		})
	}
	err := attempt()
	// safety: two accounts racing to link one provider account meet on the identities key; the loser rereads and refuses.
	if isUniqueViolation(err) {
		err = attempt()
	}
	if err != nil {
		return Identity{}, err
	}
	return out, nil
}

func linkAccountIdentityTx(ctx context.Context, tx *storeTx, claim capability.IdentityLink, p SignInProfile) (Identity, error) {
	if p.Provider != claim.Provider || p.Subject == "" || !p.EmailVerified {
		return Identity{}, identityLinkInvalid()
	}
	if err := identityLinkAuthorityTx(ctx, tx, claim); err != nil {
		return Identity{}, err
	}
	var binding string
	var completed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT binding,completed_at FROM flow_nonces WHERE kind='identity_link' AND nonce=?`+tx.forUpdate(), claim.Nonce).Scan(&binding, &completed)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (completed.Valid || binding != identityLinkBinding(claim)) {
		return Identity{}, identityLinkInvalid()
	}
	if err != nil {
		return Identity{}, err
	}
	id, err := linkIdentityTx(ctx, tx, claim.AccountID, p, tx.now())
	if err != nil {
		return Identity{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE flow_nonces SET completed_at=? WHERE kind='identity_link' AND nonce=?`, tx.now().UnixNano(), claim.Nonce); err != nil {
		return Identity{}, err
	}
	return id, nil
}

func identityLinkAuthorityTx(ctx context.Context, tx *storeTx, claim capability.IdentityLink) error {
	now := tx.now()
	if claim.Authority.Kind != capability.AuthoritySession || claim.Authority.ID == "" || claim.AccountID == "" || claim.Nonce == "" ||
		claim.Provider != ProviderGoogle && claim.Provider != ProviderGitHub || !claim.ExpiresAt.After(now) {
		return identityLinkInvalid()
	}
	if err := acquireLockSet(ctx, tx, func() lockSet {
		if _, err := accountTx(ctx, tx, claim.AccountID); err != nil {
			return lockSet{err: err}
		}
		set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{claim.Authority})
		set.add(lockAccount, true, claim.AccountID)
		return set
	}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return identityLinkInvalid()
		}
		return err
	}
	principal, err := tx.ResolveAuthority(ctx, claim.Authority)
	if err != nil {
		return err
	}
	c := principal.Capability
	if !principal.DirectAccountSession() || c.Subject.ID != claim.AccountID {
		return identityLinkInvalid()
	}
	if now.Before(c.IssuedAt) || now.Sub(c.IssuedAt) > IdentityChangeSignInWindow {
		return apierr.Authorization(apierr.ReasonReauthRequired, "linking needs a sign-in from the last 10 minutes; sign out, sign in again and retry")
	}
	return nil
}

func identityLinkBinding(claim capability.IdentityLink) string {
	encoded, _ := json.Marshal([]string{claim.Authority.ID, claim.AccountID, claim.Provider, strconv.FormatInt(claim.ExpiresAt.UnixNano(), 10)})
	return string(encoded)
}

func identityLinkInvalid() error {
	return &apierr.Error{Code: apierr.Forbidden, Message: "this link could not be verified; start linking again", Details: map[string]any{"reason": "link_state_invalid"}}
}

func linkIdentityTx(ctx context.Context, tx *storeTx, accountID string, p SignInProfile, now time.Time) (Identity, error) {
	p.Email = NormalizeEmail(p.Email)
	if _, err := accountTx(ctx, tx, accountID); err != nil {
		return Identity{}, err
	}
	var holder string
	err := tx.QueryRowContext(ctx,
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
	return Identity{
		Provider: p.Provider, Subject: p.Subject, Email: p.Email, Linked: true,
		CreatedAt: time.Unix(0, at).UTC(),
	}, nil
}

func (op *Operator) unlinkAccountIdentityTx(ctx context.Context, tx *storeTx, accountID, provider, keepHash string, now time.Time) (int64, error) {
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
	return acquireLockSet(ctx, tx, func() lockSet {
		var set lockSet
		set.add(lockAccount, true, accountID)
		return set
	})
}

func (s *Store) consumeFlowNonce(ctx context.Context, kind, nonce string, expires time.Time) (bool, error) {
	used := false
	err := s.inTx(ctx, func(tx *storeTx) error {
		var err error
		used, err = consumeFlowNonceTx(ctx, tx, kind, nonce, expires)
		return err
	})
	return err == nil && used, err
}

func consumeFlowNonceTx(ctx context.Context, tx *storeTx, kind, nonce string, expires time.Time) (bool, error) {
	if nonce == "" || !expires.After(tx.now()) {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO flow_nonces(kind,nonce,expires_at) VALUES (?,?,?) ON CONFLICT(kind,nonce) DO NOTHING`, kind, nonce, expires.UnixNano())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// IdentityLinkStateKey is the key a controller signs link-flow state with.
// It derives from the deployment's session key, so every replica agrees on
// it and rotating that key voids flows in progress along with sessions.
func (o *Operator) IdentityLinkStateKey(ctx context.Context) ([]byte, error) {
	key, err := o.csrfSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("sparkwing identity link state v1"))
	return mac.Sum(nil), nil
}
