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
	"log/slog"
	"time"
)

// Session is one row in the sessions table. ID holds the raw session id the
// caller presented; the table keys rows by its digest.
type Session struct {
	ID        string
	Principal string
	Scopes    []string
	// Team is the team the session acts for: the token's team for a token
	// session; an account session's moves when the account switches.
	Team Team
	// AccountID is set on a session opened by an identity-provider sign-in.
	// Its scopes are not stored: they come from the account's membership in
	// Team on every request.
	AccountID string
	// TokenPrefix names the operator token a token session was opened with.
	// Its scopes and team are read from that token's row on every lookup.
	TokenPrefix string
	CSRFToken   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	LastUsedAt  *time.Time
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

func (s *Store) csrfSigningKey(ctx context.Context) ([]byte, error) {
	s.csrfKeyMu.Lock()
	defer s.csrfKeyMu.Unlock()
	if len(s.csrfKey) > 0 {
		return s.csrfKey, nil
	}
	stored, found, err := s.storedCSRFKey(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		if stored, err = s.mintCSRFKey(ctx); err != nil {
			return nil, err
		}
	}
	key, err := hex.DecodeString(stored)
	if err != nil || len(key) == 0 {
		return nil, errors.New("sessions: malformed csrf key")
	}
	s.csrfKey = key
	return key, nil
}

// safety: reading before writing keeps a read-only store, and every process past the first, off the write path.
func (s *Store) storedCSRFKey(ctx context.Context) (string, bool, error) {
	var stored string
	err := s.queryRow(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeySessionCSRFKey,
	).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("sessions: read csrf key: %w", err)
	}
	return stored, true, nil
}

func (s *Store) mintCSRFKey(ctx context.Context) (string, error) {
	minted := make([]byte, sha256.Size)
	if _, err := rand.Read(minted); err != nil {
		return "", err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", fmt.Errorf("sessions: begin csrf key: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// safety: DO NOTHING keeps a concurrent minter's key, so every process derives the same token for a session.
	res, err := tx.Exec(
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO NOTHING`,
		metaKeySessionCSRFKey, hex.EncodeToString(minted), time.Now().UnixNano(),
	)
	if err != nil {
		return "", fmt.Errorf("sessions: persist csrf key: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// safety: a fresh key cannot sign the tokens live sessions carry, so key and sessions rotate as one.
		if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
			return "", fmt.Errorf("sessions: rotate on new csrf key: %w", err)
		}
	}
	var stored string
	if err := tx.QueryRow(
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeySessionCSRFKey,
	).Scan(&stored); err != nil {
		return "", fmt.Errorf("sessions: read csrf key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("sessions: commit csrf key: %w", err)
	}
	return stored, nil
}

func (s *Store) deriveCSRFToken(ctx context.Context, rawSession string) (string, error) {
	key, err := s.csrfSigningKey(ctx)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(rawSession))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func rehashSessions(ctx context.Context, tx *storeTx) error {
	// safety: pre-digest rows hold replayable session ids, so the migration drops them rather than converting them.
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	cols, err := tableColumns(ctx, tx, "sessions")
	if err != nil {
		return err
	}
	if !cols["csrf_token"] {
		return nil
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE sessions DROP COLUMN csrf_token`)
	return err
}

// CreateTokenSession opens a browser session for the holder of the token
// whose prefix is tokenPrefix, acting as principal in team. It returns the raw
// session id and its CSRF token. The row stores no scopes: every lookup reads
// them from the token's row, so revoking or expiring the token ends the
// session as well.
func (s *Store) CreateTokenSession(ctx context.Context, tokenPrefix, principal string, team Team, ttl time.Duration, now time.Time) (rawSession string, sess *Session, err error) {
	if tokenPrefix == "" || principal == "" {
		return "", nil, errors.New("sessions: token prefix and principal required")
	}
	if ttl <= 0 {
		return "", nil, errors.New("sessions: ttl must be positive")
	}
	rawSession, err = newSessionID()
	if err != nil {
		return "", nil, err
	}
	csrfToken, err := s.deriveCSRFToken(ctx, rawSession)
	if err != nil {
		return "", nil, err
	}
	expires := now.Add(ttl).UTC()
	// safety: an empty scopes column leaves a binary that predates token sessions holding no scope for this row.
	if _, err = s.exec(ctx, `
        INSERT INTO sessions (hash, team, principal, scopes, token_prefix, created_at, expires_at)
        VALUES (?, ?, ?, '', ?, ?, ?)
    `, sessionDigest(rawSession), string(NormalizeTeam(team)), principal, tokenPrefix, now.UTC().Unix(), expires.Unix()); err != nil {
		return "", nil, fmt.Errorf("sessions: insert: %w", err)
	}
	return rawSession, &Session{
		ID:          rawSession,
		Principal:   principal,
		Team:        NormalizeTeam(team),
		TokenPrefix: tokenPrefix,
		CSRFToken:   csrfToken,
		CreatedAt:   now.UTC(),
		ExpiresAt:   expires,
	}, nil
}

func newSessionID() (string, error) {
	b := make([]byte, SessionIDLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// LookupSession resolves a raw session id; bumps last_used_at on hit.
func (s *Store) LookupSession(ctx context.Context, rawSession string, now time.Time) (*Session, error) {
	return s.lookupSession(ctx, rawSession, now, 0, 0)
}

// LookupSessionAndRenew resolves a live session and extends its expiry from
// this use. A row past maxLifetime is returned without renewal for its caller
// to revoke.
func (s *Store) LookupSessionAndRenew(ctx context.Context, rawSession string, now time.Time, ttl, maxLifetime time.Duration) (*Session, error) {
	if ttl <= 0 {
		return nil, errors.New("sessions: ttl must be positive")
	}
	return s.lookupSession(ctx, rawSession, now, ttl, maxLifetime)
}

func (s *Store) lookupSession(ctx context.Context, rawSession string, now time.Time, ttl, maxLifetime time.Duration) (*Session, error) {
	if rawSession == "" {
		return nil, errors.New("empty session")
	}
	digest := sessionDigest(rawSession)
	// safety: minting a key drops every session it cannot sign for, so resolve the key before reading the row.
	csrfToken, err := s.deriveCSRFToken(ctx, rawSession)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionBackend, err)
	}
	row := s.queryRow(ctx, `
        SELECT s.principal, s.team, s.account_id, s.token_prefix,
               s.created_at, s.expires_at, s.last_used_at,
               t.prefix, t.team, t.scopes, t.expires_at, t.revoked_at
          FROM sessions s
          LEFT JOIN tokens t ON s.token_prefix <> '' AND t.prefix = s.token_prefix
         WHERE s.hash = ?
    `, digest)

	var sess Session
	var team string
	var lastUsed, tokExpires, tokRevoked sql.NullInt64
	var tokPrefix, tokTeam, tokScopes sql.NullString
	var created, expires int64
	if err := row.Scan(
		&sess.Principal, &team, &sess.AccountID, &sess.TokenPrefix,
		&created, &expires, &lastUsed,
		&tokPrefix, &tokTeam, &tokScopes, &tokExpires, &tokRevoked,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("unknown session")
		}
		return nil, fmt.Errorf("%w: read session: %w", ErrSessionBackend, err)
	}
	sess.ID = rawSession
	sess.Team = Team(team)
	if sess.AccountID == "" {
		// safety: a session that names neither an account nor a live token carries no authority, so it fails
		// rather than falling back to the scopes column a binary predating token sessions wrote.
		if !tokPrefix.Valid {
			return nil, errors.New("unknown session")
		}
		if err := TokenEnded(nullableUnix(tokRevoked), nullableUnix(tokExpires), now); err != nil {
			return nil, fmt.Errorf("session token: %w", err)
		}
		sess.Scopes = splitScopes(tokScopes.String)
		sess.Team = Team(tokTeam.String)
	}
	sess.CreatedAt = time.Unix(created, 0).UTC()
	sess.ExpiresAt = time.Unix(expires, 0).UTC()
	if lastUsed.Valid {
		ts := time.Unix(lastUsed.Int64, 0).UTC()
		sess.LastUsedAt = &ts
	}
	if !now.Before(sess.ExpiresAt) {
		return nil, errors.New("session expired")
	}
	sess.CSRFToken = csrfToken
	if ttl > 0 {
		if sess.CreatedAt.After(now) || maxLifetime > 0 && !now.Before(sess.CreatedAt.Add(maxLifetime)) {
			return &sess, nil
		}
		renewed := now.Add(ttl)
		if maxLifetime > 0 {
			limit := sess.CreatedAt.Add(maxLifetime)
			if renewed.After(limit) {
				renewed = limit
			}
		}
		result, err := s.exec(ctx, `UPDATE sessions
			SET last_used_at = CASE WHEN last_used_at > ? THEN last_used_at ELSE ? END,
				expires_at = CASE
				WHEN expires_at > ? THEN expires_at
				ELSE ? END
			WHERE hash = ? AND expires_at > ?`,
			now.UTC().Unix(), now.UTC().Unix(), renewed.Unix(), renewed.Unix(), digest, now.UTC().Unix())
		if err != nil {
			return nil, fmt.Errorf("%w: renew session: %w", ErrSessionBackend, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("%w: renew session rows: %w", ErrSessionBackend, err)
		}
		if rows == 0 {
			return nil, errors.New("unknown session")
		}
		if sess.ExpiresAt.Before(renewed) {
			sess.ExpiresAt = renewed
		}
	} else {
		if _, err := s.exec(ctx,
			`UPDATE sessions SET last_used_at = ? WHERE hash = ?`,
			now.UTC().Unix(), digest,
		); err != nil {
			slog.Warn("sessions: recording a session's last use failed", "err", err)
		}
	}
	if ttl == 0 || sess.LastUsedAt == nil || sess.LastUsedAt.Before(now) {
		ts := now.UTC()
		sess.LastUsedAt = &ts
	}
	return &sess, nil
}

// DeleteSession removes the session by its raw id. Idempotent.
func (s *Store) DeleteSession(rawSession string) error {
	_, err := s.execNoCtx(`DELETE FROM sessions WHERE hash = ?`, sessionDigest(rawSession))
	return err
}

// ExpireSessions purges rows whose expires_at is past.
func (s *Store) ExpireSessions(now time.Time) (int64, error) {
	res, err := s.execNoCtx(
		`DELETE FROM sessions WHERE expires_at <= ?`,
		now.UTC().Unix(),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func nullableUnix(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}
