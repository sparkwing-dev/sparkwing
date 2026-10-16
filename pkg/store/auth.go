package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Session carries presentation and current authority after a committed lookup.
type Session struct {
	ID               string
	Principal        string
	Scopes           []string
	Team             Team
	AccountID        string
	CapabilityPrefix string
	CSRFToken        string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	LastUsedAt       *time.Time
}

// SessionIDLen is the raw byte length before base64.
const SessionIDLen = 32

const metaKeySessionCSRFKey = "session_csrf_key"

func sessionDigest(rawSession string) string {
	sum := sha256.Sum256([]byte(rawSession))
	return hex.EncodeToString(sum[:])
}

// ErrSessionBackend marks a session lookup that failed for an infrastructure
// reason rather than an unknown or expired session. Callers answer 5xx for it
// so a browser keeps cookies a working backend would still resolve.
var ErrSessionBackend = errors.New("sessions: backend unavailable")

func (s *Store) deriveCSRFToken(ctx context.Context, rawSession string) (string, error) {
	key, err := s.csrfSigningKey(ctx)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(rawSession))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// CreateCapabilitySession opens a session with an account capability as its revocation parent.
func (s *Store) CreateCapabilitySession(ctx context.Context, prefix string, ttl time.Duration) (string, *Session, error) {
	if prefix == "" || ttl <= 0 {
		return "", nil, fmt.Errorf("%w: capability and positive ttl required", ErrInvalidInput)
	}
	raw, err := newSessionID()
	if err != nil {
		return "", nil, err
	}
	csrf, err := s.deriveCSRFToken(ctx, raw)
	if err != nil {
		return "", nil, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", nil, err
	}
	defer rollbackOrLog(tx)
	p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	if err != nil {
		return "", nil, err
	}
	c := p.Capability
	if c.Subject.Kind != capability.SubjectAccount || c.Team == "" || c.Audience != capability.Controller || c.Binding.Kind != capability.BindingNone || c.Unit != "" || c.Data != nil {
		return "", nil, authorityError(apierr.Forbidden, "session requires an unbound account capability without data scope")
	}
	var email string
	if err := tx.QueryRowContext(ctx, `SELECT email FROM accounts WHERE id=?`, c.Subject.ID).Scan(&email); err != nil {
		return "", nil, err
	}
	now := tx.now()
	expires := now.Add(ttl)
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(hash,team,principal,scopes,account_id,capability_prefix,created_at,expires_at)
 VALUES (?,?,?,'',?,?,?,?)`, sessionDigest(raw), c.Team, email, c.Subject.ID, prefix, now.UnixNano(), expires.UnixNano()); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	session := &Session{ID: raw, Principal: email, Team: Team(c.Team), AccountID: c.Subject.ID, CapabilityPrefix: prefix, CSRFToken: csrf, CreatedAt: now, ExpiresAt: expires}
	for _, grant := range c.Grants {
		session.Scopes = append(session.Scopes, string(grant))
	}
	return raw, session, nil
}

func newSessionID() (string, error) {
	b := make([]byte, SessionIDLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// LookupSession resolves a raw session id; bumps last_used_at on hit.
func (s *Store) LookupSession(ctx context.Context, rawSession string) (*Session, error) {
	return s.lookupSession(ctx, rawSession, 0, 0)
}

// LookupSessionAndRenew resolves a live session and extends its expiry from
// this use. A row past maxLifetime is returned without renewal for its caller
// to revoke.
func (s *Store) LookupSessionAndRenew(ctx context.Context, rawSession string, ttl, maxLifetime time.Duration) (*Session, error) {
	if ttl <= 0 {
		return nil, errors.New("sessions: ttl must be positive")
	}
	return s.lookupSession(ctx, rawSession, ttl, maxLifetime)
}

func (s *Store) lookupSession(ctx context.Context, rawSession string, ttl, maxLifetime time.Duration) (*Session, error) {
	if rawSession == "" {
		return nil, errors.New("empty session")
	}
	csrfToken, err := s.deriveCSRFToken(ctx, rawSession)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	defer rollbackOrLog(tx)
	digest := sessionDigest(rawSession)
	var sess Session
	var created, expires int64
	var lastUsed sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT principal,COALESCE(team,''),account_id,capability_prefix,created_at,expires_at,last_used_at
 FROM sessions WHERE hash=?`+tx.forUpdate(), digest).Scan(&sess.Principal, &sess.Team, &sess.AccountID, &sess.CapabilityPrefix, &created, &expires, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("unknown session")
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthoritySession, ID: digest}, true)
	if err != nil {
		var failure *apierr.Error
		if errors.As(err, &failure) && failure.Code == apierr.Unauthenticated {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	now := tx.now()
	sess.ID, sess.CSRFToken = rawSession, csrfToken
	sess.CreatedAt, sess.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, expires).UTC()
	for _, grant := range chain[0].principal.Capability.Grants {
		sess.Scopes = append(sess.Scopes, string(grant))
	}
	last := stateTime(lastUsed)
	if last == nil || last.Before(now) {
		last = &now
	}
	sess.LastUsedAt = last
	if ttl > 0 && !sess.CreatedAt.After(now) && (maxLifetime <= 0 || now.Before(sess.CreatedAt.Add(maxLifetime))) {
		renewed := now.Add(ttl)
		if maxLifetime > 0 && renewed.After(sess.CreatedAt.Add(maxLifetime)) {
			renewed = sess.CreatedAt.Add(maxLifetime)
		}
		if renewed.After(sess.ExpiresAt) {
			sess.ExpiresAt = renewed
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET last_used_at=?,expires_at=? WHERE hash=? AND revoked_at IS NULL AND expires_at>?`, last.UnixNano(), sess.ExpiresAt.UnixNano(), digest, now.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	if n != 1 {
		return nil, errors.New("session has ended")
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	return &sess, nil
}

// DeleteSession removes the session by its raw id. Idempotent.
func (s *Store) DeleteSession(rawSession string) error {
	_, err := s.execNoCtx(`DELETE FROM sessions WHERE hash = ?`, sessionDigest(rawSession))
	return err
}
