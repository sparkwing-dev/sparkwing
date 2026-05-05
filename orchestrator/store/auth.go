package store

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Session ids stored plaintext in the `hash` column (historical
// name); 256-bit entropy + 12h TTL bounds DB-dump risk. Hashing would
// require server-secret HMAC or argon2-on-every-lookup.

// Session is one row in the sessions table.
type Session struct {
	ID         string // raw id; also the primary key
	Principal  string
	Scopes     []string
	CSRFToken  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

// User is one row in the users table.
type User struct {
	Name        string
	PWHash      string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// SessionIDLen is the raw byte length before base64.
const SessionIDLen = 32

// CreateSession returns a raw session id + CSRF token.
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

	csrfBytes := make([]byte, 24)
	if _, err := rand.Read(csrfBytes); err != nil {
		return "", "", nil, err
	}
	csrfToken = base64.RawURLEncoding.EncodeToString(csrfBytes)

	expires := now.Add(ttl).UTC()
	scopeStr := joinScopes(scopes)
	_, err = s.db.Exec(`
        INSERT INTO sessions (hash, principal, scopes, csrf_token, created_at, expires_at)
        VALUES (?, ?, ?, ?, ?, ?)
    `, rawSession, principal, scopeStr, csrfToken, now.UTC().Unix(), expires.Unix())
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
	row := s.db.QueryRow(`
        SELECT principal, scopes, csrf_token,
               created_at, expires_at, last_used_at
          FROM sessions
         WHERE hash = ?
    `, rawSession)

	var sess Session
	var scopes string
	var lastUsed sql.NullInt64
	var created, expires int64
	if err := row.Scan(
		&sess.Principal, &scopes, &sess.CSRFToken,
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
	_, _ = s.db.Exec(
		`UPDATE sessions SET last_used_at = ? WHERE hash = ?`,
		now.UTC().Unix(), rawSession,
	)
	ts := now.UTC()
	sess.LastUsedAt = &ts
	return &sess, nil
}

// DeleteSession removes the session by its raw id. Idempotent.
func (s *Store) DeleteSession(rawSession string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE hash = ?`, rawSession)
	return err
}

// ExpireSessions purges rows whose expires_at is past.
func (s *Store) ExpireSessions(now time.Time) (int64, error) {
	res, err := s.db.Exec(
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
	_, err := s.db.Exec(
		`UPDATE sessions SET expires_at = ? WHERE hash = ?`,
		expires, rawSession,
	)
	return err
}

// --- User CRUD (password-authenticated principals) ---

// CreateUser inserts a user with an argon2id-hashed password.
func (s *Store) CreateUser(name, password string, now time.Time) (*User, error) {
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
	_, err = s.db.Exec(
		`INSERT INTO users (name, pw_hash, created_at) VALUES (?, ?, ?)`,
		name, pwHash, now.UTC().Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("users: insert: %w", err)
	}
	return &User{
		Name:      name,
		PWHash:    pwHash,
		CreatedAt: now.UTC(),
	}, nil
}

// ErrBootstrapClosed: users table already has rows.
var ErrBootstrapClosed = errors.New("users: bootstrap closed (table not empty)")

// CreateFirstUser race-safely inserts the first admin in one txn.
func (s *Store) CreateFirstUser(name, password string, now time.Time) (*User, error) {
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
	defer tx.Rollback()

	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return nil, fmt.Errorf("users: count: %w", err)
	}
	if count > 0 {
		return nil, ErrBootstrapClosed
	}
	if _, err := tx.Exec(
		`INSERT INTO users (name, pw_hash, created_at) VALUES (?, ?, ?)`,
		name, pwHash, now.UTC().Unix(),
	); err != nil {
		return nil, fmt.Errorf("users: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("users: commit: %w", err)
	}
	return &User{
		Name:      name,
		PWHash:    pwHash,
		CreatedAt: now.UTC(),
	}, nil
}

// CountUsers returns the row count.
func (s *Store) CountUsers() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// VerifyUser checks credentials in constant time.
func (s *Store) VerifyUser(name, password string, now time.Time) (*User, error) {
	u, err := s.lookupUser(name)
	if err != nil {
		// Dummy hash to avoid timing leak.
		_, _ = hashPassword(password)
		return nil, errors.New("invalid username or password")
	}
	ok, err := verifyPassword(password, u.PWHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("invalid username or password")
	}
	_, _ = s.db.Exec(
		`UPDATE users SET last_login_at = ? WHERE name = ?`,
		now.UTC().Unix(), name,
	)
	ts := now.UTC()
	u.LastLoginAt = &ts
	return u, nil
}

// ListUsers returns every user (for audit).
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`
        SELECT name, pw_hash, created_at, last_login_at
          FROM users
         ORDER BY created_at DESC
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		var lastLogin sql.NullInt64
		var created int64
		if err := rows.Scan(&u.Name, &u.PWHash, &created, &lastLogin); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0).UTC()
		if lastLogin.Valid {
			ts := time.Unix(lastLogin.Int64, 0).UTC()
			u.LastLoginAt = &ts
		}
		out = append(out, u)
	}
	return out, nil
}

// DeleteUser removes the user; existing sessions remain valid.
func (s *Store) DeleteUser(name string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("user not found")
	}
	return nil
}

// --- helpers ---

func (s *Store) lookupUser(name string) (*User, error) {
	row := s.db.QueryRow(`
        SELECT name, pw_hash, created_at, last_login_at
          FROM users
         WHERE name = ?
    `, name)
	var u User
	var lastLogin sql.NullInt64
	var created int64
	if err := row.Scan(&u.Name, &u.PWHash, &created, &lastLogin); err != nil {
		return nil, err
	}
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

// hashPassword/verifyPassword reuse the argon2id token params.
func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
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
	cand := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(cand, key) == 1, nil
}
