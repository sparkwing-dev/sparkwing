package store

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Token kinds (stored in the `kind` column).
const (
	TokenKindUser    = "user"
	TokenKindRunner  = "runner"
	TokenKindService = "service"
)

// Token prefix markers; raw is `<prefix>_<entropy>`.
const (
	TokenPrefixUser    = "swu"
	TokenPrefixRunner  = "swr"
	TokenPrefixService = "sws"
)

// Argon2id params: ~8-15ms on arm64; 64MiB memory.
const (
	argonTime    = uint32(1)
	argonMemory  = uint32(64 * 1024)
	argonThreads = uint8(4)
	argonKeyLen  = uint32(32)
	argonSaltLen = 16
)

// PrefixLen: chars 0-2 = kind marker, char 3 = underscore, chars
// 4-11 = 48 bits of entropy.
const PrefixLen = 12

// Token is one row in the tokens table.
type Token struct {
	Hash       string
	Prefix     string
	Principal  string
	Kind       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	ReplacedBy string // non-empty when this token has been rotated
}

// IsValid reports whether the token is usable at `now`.
func (t *Token) IsValid(now time.Time) bool {
	if t.RevokedAt != nil && !now.Before(*t.RevokedAt) {
		return false
	}
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return false
	}
	return true
}

// HasScope reports exact-string membership.
func (t *Token) HasScope(scope string) bool {
	return slices.Contains(t.Scopes, scope)
}

// TokenKindFromPrefix maps the 3-char marker; "" = unrecognized.
func TokenKindFromPrefix(raw string) string {
	if len(raw) < 5 || raw[3] != '_' {
		return ""
	}
	switch raw[:3] {
	case TokenPrefixUser:
		return TokenKindUser
	case TokenPrefixRunner:
		return TokenKindRunner
	case TokenPrefixService:
		return TokenKindService
	default:
		return ""
	}
}

// CreateToken mints a token. Returns the RAW string only once; the
// hash is one-way.
func (s *Store) CreateToken(principal, kind string, scopes []string, ttl time.Duration, now time.Time) (raw string, tok *Token, err error) {
	if principal == "" {
		return "", nil, errors.New("tokens: principal is required")
	}
	prefix, ok := prefixForKind(kind)
	if !ok {
		return "", nil, fmt.Errorf("tokens: unknown kind %q", kind)
	}
	raw, err = mintRaw(prefix)
	if err != nil {
		return "", nil, err
	}
	hash, err := hashToken(raw)
	if err != nil {
		return "", nil, err
	}
	var expires *time.Time
	if ttl > 0 {
		t := now.Add(ttl)
		expires = &t
	}

	scopeStr := strings.Join(dedupeScopes(scopes), ",")
	_, err = s.db.Exec(`
        INSERT INTO tokens (hash, prefix, principal, kind, scopes, created_at, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
    `,
		hash, raw[:PrefixLen], principal, kind, scopeStr,
		now.UTC().Unix(),
		expiresUnix(expires),
	)
	if err != nil {
		return "", nil, fmt.Errorf("tokens: insert: %w", err)
	}

	tok = &Token{
		Hash:      hash,
		Prefix:    raw[:PrefixLen],
		Principal: principal,
		Kind:      kind,
		Scopes:    dedupeScopes(scopes),
		CreatedAt: now.UTC(),
		ExpiresAt: expires,
	}
	return raw, tok, nil
}

// LookupToken authenticates and bumps last_used_at. Materialize the
// candidate list before any follow-up Exec — MaxOpenConns=1 will
// deadlock if a cursor is still open.
func (s *Store) LookupToken(raw string, now time.Time) (*Token, error) {
	if len(raw) < PrefixLen {
		return nil, errors.New("invalid token")
	}
	prefix := raw[:PrefixLen]

	candidates, err := s.selectTokensByPrefix(prefix)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("unknown token")
	}

	for i := range candidates {
		t := &candidates[i]
		ok, err := verifyToken(raw, t.Hash)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if !t.IsValid(now) {
			return nil, errors.New("token is revoked or expired")
		}
		// Touch last_used_at. Best-effort: a failed UPDATE doesn't
		// invalidate the auth result.
		_, _ = s.db.Exec(
			`UPDATE tokens SET last_used_at = ? WHERE hash = ?`,
			now.UTC().Unix(), t.Hash,
		)
		ts := now.UTC()
		t.LastUsedAt = &ts
		return t, nil
	}
	return nil, errors.New("unknown token")
}

// selectTokensByPrefix materializes all rows matching the prefix into
// memory, closing the cursor before returning. Used by LookupToken
// and LookupTokenByPrefix; centralizes the row-scan code + the
// MaxOpenConns=1 cursor-lifetime discipline.
func (s *Store) selectTokensByPrefix(prefix string) ([]Token, error) {
	rows, err := s.db.Query(`
        SELECT hash, prefix, principal, kind, scopes,
               created_at, expires_at, last_used_at, revoked_at,
               COALESCE(replaced_by, '')
          FROM tokens
         WHERE prefix = ?
    `, prefix)
	if err != nil {
		return nil, fmt.Errorf("tokens: query: %w", err)
	}
	defer rows.Close()

	var out []Token
	for rows.Next() {
		var t Token
		var scopes string
		var expiresAt, lastUsedAt, revokedAt sql.NullInt64
		var created int64
		if err := rows.Scan(
			&t.Hash, &t.Prefix, &t.Principal, &t.Kind, &scopes,
			&created, &expiresAt, &lastUsedAt, &revokedAt,
			&t.ReplacedBy,
		); err != nil {
			return nil, err
		}
		t.Scopes = splitScopes(scopes)
		t.CreatedAt = time.Unix(created, 0).UTC()
		if expiresAt.Valid {
			et := time.Unix(expiresAt.Int64, 0).UTC()
			t.ExpiresAt = &et
		}
		if lastUsedAt.Valid {
			lt := time.Unix(lastUsedAt.Int64, 0).UTC()
			t.LastUsedAt = &lt
		}
		if revokedAt.Valid {
			rt := time.Unix(revokedAt.Int64, 0).UTC()
			t.RevokedAt = &rt
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken sets revoked_at=now; row is kept for audit.
func (s *Store) RevokeToken(prefix string, now time.Time) error {
	res, err := s.db.Exec(
		`UPDATE tokens SET revoked_at = ? WHERE prefix = ? AND revoked_at IS NULL`,
		now.UTC().Unix(), prefix,
	)
	if err != nil {
		return fmt.Errorf("tokens: revoke: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("token not found or already revoked")
	}
	if n > 1 {
		return fmt.Errorf("tokens: prefix %q matched %d rows, aborting", prefix, n)
	}
	return nil
}

// ListTokens returns matching rows. Empty kind = all.
func (s *Store) ListTokens(kind string, includeRevoked bool) ([]Token, error) {
	q := `
        SELECT hash, prefix, principal, kind, scopes,
               created_at, expires_at, last_used_at, revoked_at,
               COALESCE(replaced_by, '')
          FROM tokens
    `
	args := []any{}
	where := []string{}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if !includeRevoked {
		where = append(where, "revoked_at IS NULL")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Token
	for rows.Next() {
		var t Token
		var scopes string
		var expiresAt, lastUsedAt, revokedAt sql.NullInt64
		var created int64
		if err := rows.Scan(
			&t.Hash, &t.Prefix, &t.Principal, &t.Kind, &scopes,
			&created, &expiresAt, &lastUsedAt, &revokedAt,
			&t.ReplacedBy,
		); err != nil {
			return nil, err
		}
		t.Scopes = splitScopes(scopes)
		t.CreatedAt = time.Unix(created, 0).UTC()
		if expiresAt.Valid {
			et := time.Unix(expiresAt.Int64, 0).UTC()
			t.ExpiresAt = &et
		}
		if lastUsedAt.Valid {
			lt := time.Unix(lastUsedAt.Int64, 0).UTC()
			t.LastUsedAt = &lt
		}
		if revokedAt.Valid {
			rt := time.Unix(revokedAt.Int64, 0).UTC()
			t.RevokedAt = &rt
		}
		out = append(out, t)
	}
	return out, nil
}

// LookupTokenByPrefix returns the first matching row.
func (s *Store) LookupTokenByPrefix(prefix string) (*Token, error) {
	rows, err := s.selectTokensByPrefix(prefix)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("token not found")
	}
	return &rows[0], nil
}

// RotateToken mints a peer and revokes the original at now+grace.
func (s *Store) RotateToken(prefix string, grace, ttl time.Duration, now time.Time) (raw string, newTok, oldTok *Token, err error) {
	oldTok, err = s.LookupTokenByPrefix(prefix)
	if err != nil {
		return "", nil, nil, err
	}
	if oldTok.RevokedAt != nil {
		return "", nil, nil, errors.New("token is already revoked")
	}

	raw, newTok, err = s.CreateToken(oldTok.Principal, oldTok.Kind, oldTok.Scopes, ttl, now)
	if err != nil {
		return "", nil, nil, err
	}

	revokeAt := now.Add(grace).UTC()
	_, err = s.db.Exec(
		`UPDATE tokens SET revoked_at = ?, replaced_by = ? WHERE prefix = ?`,
		revokeAt.Unix(), newTok.Prefix, prefix,
	)
	if err != nil {
		return "", nil, nil, fmt.Errorf("tokens: rotate update: %w", err)
	}
	oldTok.RevokedAt = &revokeAt
	oldTok.ReplacedBy = newTok.Prefix
	return raw, newTok, oldTok, nil
}

// --- helpers ---

func prefixForKind(kind string) (string, bool) {
	switch kind {
	case TokenKindUser:
		return TokenPrefixUser, true
	case TokenKindRunner:
		return TokenPrefixRunner, true
	case TokenKindService:
		return TokenPrefixService, true
	default:
		return "", false
	}
}

func mintRaw(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken returns "argon2id$<saltHex>$<keyHex>" with a fresh salt.
func hashToken(raw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(raw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// verifyToken returns true iff raw hashes to stored.
func verifyToken(raw, stored string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false, errors.New("tokens: malformed hash")
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false, err
	}
	key, err := hex.DecodeString(parts[2])
	if err != nil {
		return false, err
	}
	cand := argon2.IDKey([]byte(raw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(cand, key) == 1, nil
}

func dedupeScopes(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func splitScopes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func expiresUnix(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}
