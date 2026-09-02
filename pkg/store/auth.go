package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Session is one row in the sessions table. ID holds the raw session id the
// caller presented; the table keys rows by its digest.
type Session struct {
	ID         string
	Principal  string
	Scopes     []string
	CSRFToken  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

// User is one row in the users table. Scopes is the authorization set a
// browser session inherits when this user signs in.
type User struct {
	Name        string
	PWHash      string
	Scopes      []string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// SessionIDLen is the raw byte length before base64.
const SessionIDLen = 32

// ErrInvalidCredentials reports a username that does not exist or a
// password that does not match. One error covers both so the response
// is not a user-existence oracle.
var ErrInvalidCredentials = errors.New("invalid username or password")

const metaKeySessionCSRFKey = "session_csrf_key"

func sessionDigest(rawSession string) string {
	sum := sha256.Sum256([]byte(rawSession))
	return hex.EncodeToString(sum[:])
}

func (s *Store) csrfSigningKey() ([]byte, error) {
	s.csrfKeyMu.Lock()
	defer s.csrfKeyMu.Unlock()
	if len(s.csrfKey) > 0 {
		return s.csrfKey, nil
	}
	minted := make([]byte, sha256.Size)
	if _, err := rand.Read(minted); err != nil {
		return nil, err
	}
	// safety: DO NOTHING keeps a concurrent minter's key, so every process derives the same token for a session.
	if _, err := s.execNoCtx(
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO NOTHING`,
		metaKeySessionCSRFKey, hex.EncodeToString(minted), time.Now().UnixNano(),
	); err != nil {
		return nil, fmt.Errorf("sessions: persist csrf key: %w", err)
	}
	var stored string
	if err := s.queryRowNoCtx(
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeySessionCSRFKey,
	).Scan(&stored); err != nil {
		return nil, fmt.Errorf("sessions: read csrf key: %w", err)
	}
	key, err := hex.DecodeString(stored)
	if err != nil || len(key) == 0 {
		return nil, errors.New("sessions: malformed csrf key")
	}
	s.csrfKey = key
	return key, nil
}

func (s *Store) deriveCSRFToken(rawSession string) (string, error) {
	key, err := s.csrfSigningKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(rawSession))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Store) rehashSessions(ctx context.Context) error {
	// safety: pre-digest rows hold replayable session ids, so the migration drops them rather than converting them.
	if _, err := s.exec(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	cols, err := s.tableColumns("sessions")
	if err != nil {
		return err
	}
	if !cols["csrf_token"] {
		return nil
	}
	_, err = s.exec(ctx, `ALTER TABLE sessions DROP COLUMN csrf_token`)
	return err
}

// CreateSession returns a raw session id and its CSRF token. The table stores
// the session digest; the CSRF token is derived on demand and never stored.
func (s *Store) CreateSession(principal string, scopes []string, ttl time.Duration, now time.Time) (rawSession, csrfToken string, sess *Session, err error) {
	if principal == "" {
		return "", "", nil, errors.New("sessions: principal required")
	}
	if ttl <= 0 {
		return "", "", nil, errors.New("sessions: ttl must be positive")
	}
	sessBytes := make([]byte, SessionIDLen)
	if _, err := rand.Read(sessBytes); err != nil {
		return "", "", nil, err
	}
	rawSession = base64.RawURLEncoding.EncodeToString(sessBytes)

	csrfToken, err = s.deriveCSRFToken(rawSession)
	if err != nil {
		return "", "", nil, err
	}

	expires := now.Add(ttl).UTC()
	scopeStr := joinScopes(scopes)
	_, err = s.execNoCtx(`
        INSERT INTO sessions (hash, principal, scopes, created_at, expires_at)
        VALUES (?, ?, ?, ?, ?)
    `, sessionDigest(rawSession), principal, scopeStr, now.UTC().Unix(), expires.Unix())
	if err != nil {
		return "", "", nil, fmt.Errorf("sessions: insert: %w", err)
	}
	sess = &Session{
		ID:        rawSession,
		Principal: principal,
		Scopes:    dedupeScopes(scopes),
		CSRFToken: csrfToken,
		CreatedAt: now.UTC(),
		ExpiresAt: expires,
	}
	return rawSession, csrfToken, sess, nil
}

// LookupSession resolves a raw session id; bumps last_used_at on hit.
func (s *Store) LookupSession(rawSession string, now time.Time) (*Session, error) {
	if rawSession == "" {
		return nil, errors.New("empty session")
	}
	digest := sessionDigest(rawSession)
	row := s.queryRowNoCtx(`
        SELECT principal, scopes,
               created_at, expires_at, last_used_at
          FROM sessions
         WHERE hash = ?
    `, digest)

	var sess Session
	var scopes string
	var lastUsed sql.NullInt64
	var created, expires int64
	if err := row.Scan(
		&sess.Principal, &scopes,
		&created, &expires, &lastUsed,
	); err != nil {
		return nil, errors.New("unknown session")
	}
	sess.ID = rawSession
	sess.Scopes = splitScopes(scopes)
	sess.CreatedAt = time.Unix(created, 0).UTC()
	sess.ExpiresAt = time.Unix(expires, 0).UTC()
	if lastUsed.Valid {
		ts := time.Unix(lastUsed.Int64, 0).UTC()
		sess.LastUsedAt = &ts
	}
	if !now.Before(sess.ExpiresAt) {
		return nil, errors.New("session expired")
	}
	csrfToken, err := s.deriveCSRFToken(rawSession)
	if err != nil {
		return nil, err
	}
	sess.CSRFToken = csrfToken
	_, _ = s.execNoCtx(
		`UPDATE sessions SET last_used_at = ? WHERE hash = ?`,
		now.UTC().Unix(), digest,
	)
	ts := now.UTC()
	sess.LastUsedAt = &ts
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

// ExtendSession bumps expires_at to now+ttl (sliding TTL).
func (s *Store) ExtendSession(rawSession string, ttl time.Duration, now time.Time) error {
	expires := now.Add(ttl).UTC().Unix()
	_, err := s.execNoCtx(
		`UPDATE sessions SET expires_at = ? WHERE hash = ?`,
		expires, sessionDigest(rawSession),
	)
	return err
}

// CreateUser inserts a user with an argon2id-hashed password. scopes is
// the authorization set every session this user opens carries; the caller
// owns the scope vocabulary and validates it.
func (s *Store) CreateUser(name, password string, scopes []string, now time.Time) (*User, error) {
	if name == "" {
		return nil, errors.New("users: name required")
	}
	if len(password) < 8 {
		return nil, errors.New("users: password must be at least 8 characters")
	}
	pwHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	_, err = s.execNoCtx(
		`INSERT INTO users (name, pw_hash, created_at, scopes) VALUES (?, ?, ?, ?)`,
		name, pwHash, now.UTC().Unix(), joinScopes(scopes),
	)
	if err != nil {
		return nil, fmt.Errorf("users: insert: %w", err)
	}
	return &User{
		Name:      name,
		PWHash:    pwHash,
		Scopes:    dedupeScopes(scopes),
		CreatedAt: now.UTC(),
	}, nil
}

// ErrBootstrapClosed signals the users table is non-empty so first-admin
// bootstrap is no longer permitted.
var ErrBootstrapClosed = errors.New("users: bootstrap closed (table not empty)")

// CreateFirstUser race-safely inserts the first admin in one txn. scopes
// is the authorization set that account's sessions carry.
func (s *Store) CreateFirstUser(name, password string, scopes []string, now time.Time) (*User, error) {
	if name == "" {
		return nil, errors.New("users: name required")
	}
	if len(password) < 8 {
		return nil, errors.New("users: password must be at least 8 characters")
	}
	pwHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("users: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return nil, fmt.Errorf("users: count: %w", err)
	}
	if count > 0 {
		return nil, ErrBootstrapClosed
	}
	if _, err := tx.Exec(
		`INSERT INTO users (name, pw_hash, created_at, scopes) VALUES (?, ?, ?, ?)`,
		name, pwHash, now.UTC().Unix(), joinScopes(scopes),
	); err != nil {
		return nil, fmt.Errorf("users: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("users: commit: %w", err)
	}
	return &User{
		Name:      name,
		PWHash:    pwHash,
		Scopes:    dedupeScopes(scopes),
		CreatedAt: now.UTC(),
	}, nil
}

// CountUsers returns the row count.
func (s *Store) CountUsers() (int, error) {
	var n int
	if err := s.queryRowNoCtx(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// VerifyUser checks credentials in constant time.
func (s *Store) VerifyUser(name, password string, now time.Time) (*User, error) {
	u, err := s.lookupUser(name)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("users: lookup: %w", err)
		}
		// safety: the unknown-user path hashes too, so response time does not disclose which names exist.
		if _, herr := hashPassword(password); errors.Is(herr, ErrHashingBusy) {
			return nil, herr
		}
		return nil, ErrInvalidCredentials
	}
	ok, err := verifyPassword(password, u.PWHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	_, _ = s.execNoCtx(
		`UPDATE users SET last_login_at = ? WHERE name = ?`,
		now.UTC().Unix(), name,
	)
	ts := now.UTC()
	u.LastLoginAt = &ts
	return u, nil
}

// ListUsers returns every user (for audit).
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.queryNoCtx(`
        SELECT name, pw_hash, created_at, last_login_at, scopes
          FROM users
         ORDER BY created_at DESC
    `)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []User
	for rows.Next() {
		var u User
		var lastLogin sql.NullInt64
		var created int64
		var scopes string
		if err := rows.Scan(&u.Name, &u.PWHash, &created, &lastLogin, &scopes); err != nil {
			return nil, err
		}
		u.Scopes = splitScopes(scopes)
		u.CreatedAt = time.Unix(created, 0).UTC()
		if lastLogin.Valid {
			ts := time.Unix(lastLogin.Int64, 0).UTC()
			u.LastLoginAt = &ts
		}
		out = append(out, u)
	}
	return out, nil
}

// DeleteUser removes the user, deletes every session it opened, and
// revokes every token minted for it, in one transaction. It returns the
// prefixes of the tokens it revoked so the caller can drop them from an
// authentication cache.
func (s *Store) DeleteUser(name string, now time.Time) ([]string, error) {
	tx, err := s.beginTx(context.Background())
	if err != nil {
		return nil, fmt.Errorf("users: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`DELETE FROM users WHERE name = ?`, name)
	if err != nil {
		return nil, fmt.Errorf("users: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("user not found")
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE principal = ?`, name); err != nil {
		return nil, fmt.Errorf("users: delete sessions: %w", err)
	}

	ts := now.UTC().Unix()
	prefixes, err := livePrefixesForPrincipal(tx, name, ts)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE tokens SET revoked_at = ? WHERE principal = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		ts, name, ts,
	); err != nil {
		return nil, fmt.Errorf("users: revoke tokens: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("users: commit: %w", err)
	}
	return prefixes, nil
}

func livePrefixesForPrincipal(tx *storeTx, name string, ts int64) ([]string, error) {
	rows, err := tx.Query(
		`SELECT prefix FROM tokens WHERE principal = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		name, ts,
	)
	if err != nil {
		return nil, fmt.Errorf("users: select tokens: %w", err)
	}
	// safety: the cursor must close before the caller's UPDATE; a single pooled connection deadlocks otherwise.
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, err
		}
		out = append(out, prefix)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, rows.Close()
}

func (s *Store) lookupUser(name string) (*User, error) {
	row := s.queryRowNoCtx(`
        SELECT name, pw_hash, created_at, last_login_at, scopes
          FROM users
         WHERE name = ?
    `, name)
	var u User
	var lastLogin sql.NullInt64
	var created int64
	var scopes string
	if err := row.Scan(&u.Name, &u.PWHash, &created, &lastLogin, &scopes); err != nil {
		return nil, err
	}
	u.Scopes = splitScopes(scopes)
	u.CreatedAt = time.Unix(created, 0).UTC()
	if lastLogin.Valid {
		ts := time.Unix(lastLogin.Int64, 0).UTC()
		u.LastLoginAt = &ts
	}
	return &u, nil
}

func joinScopes(s []string) string {
	return strings.Join(dedupeScopes(s), ",")
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argonKey(password, salt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("argon2id$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

func verifyPassword(password, stored string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false, errors.New("malformed hash")
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false, err
	}
	key, err := hex.DecodeString(parts[2])
	if err != nil {
		return false, err
	}
	cand, err := argonKey(password, salt)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(cand, key) == 1, nil
}
