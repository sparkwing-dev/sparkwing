package store

import (
	"context"
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

const (
	argonTime    = uint32(1)
	argonMemory  = uint32(64 * 1024)
	argonThreads = uint8(4)
	argonKeyLen  = uint32(32)
	argonSaltLen = 16
)

// PrefixLen is 12: chars 0-2 = kind marker, char 3 = underscore, chars
// 4-11 = 48 bits of entropy.
const PrefixLen = 12

const mintAttempts = 5

// Bearer rejection reasons. ErrNoTokenCandidates and ErrUnknownToken
// carry the same message so the response never tells a caller whether
// a prefix exists; only ErrUnknownToken means a stored hash was
// actually compared.
var (
	ErrInvalidToken      = errors.New("invalid token")
	ErrNoTokenCandidates = errors.New("unknown token")
	ErrUnknownToken      = errors.New("unknown token")
	ErrTokenRevoked      = errors.New("token is revoked or expired")
)

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
func (s *Store) CreateToken(principal, kind string, scopes []string, ttl time.Duration, now time.Time) (string, *Token, error) {
	ctx := context.Background()
	for attempt := 1; ; attempt++ {
		raw, tok, err := createTokenRow(ctx, storeExecer{s: s}, principal, kind, scopes, ttl, now)
		if err == nil {
			return raw, tok, nil
		}
		// safety: only a prefix collision is cured by minting again, so any other unique column fails now
		if attempt < mintAttempts && isTokenPrefixCollision(err) {
			continue
		}
		return "", nil, err
	}
}

type tokenExecer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func createTokenRow(
	ctx context.Context, e tokenExecer,
	principal, kind string, scopes []string, ttl time.Duration, now time.Time,
) (string, *Token, error) {
	if principal == "" {
		return "", nil, errors.New("tokens: principal is required")
	}
	marker, ok := prefixForKind(kind)
	if !ok {
		return "", nil, fmt.Errorf("tokens: unknown kind %q", kind)
	}
	var expires *time.Time
	if ttl > 0 {
		t := now.Add(ttl)
		expires = &t
	}
	raw, err := mintRaw(marker)
	if err != nil {
		return "", nil, err
	}
	hash, err := hashToken(raw)
	if err != nil {
		return "", nil, err
	}
	scoped := dedupeScopes(scopes)
	if _, err := e.ExecContext(ctx, `
        INSERT INTO tokens (hash, prefix, principal, kind, scopes, created_at, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
    `,
		hash, raw[:PrefixLen], principal, kind, strings.Join(scoped, ","),
		now.UTC().Unix(),
		expiresUnix(expires),
	); err != nil {
		return "", nil, fmt.Errorf("tokens: insert: %w", err)
	}
	return raw, &Token{
		Hash:      hash,
		Prefix:    raw[:PrefixLen],
		Principal: principal,
		Kind:      kind,
		Scopes:    scoped,
		CreatedAt: now.UTC(),
		ExpiresAt: expires,
	}, nil
}

func isTokenPrefixCollision(err error) bool {
	return isUniqueViolation(err) &&
		strings.Contains(strings.ToLower(err.Error()), tokenPrefixColumn)
}

// LookupToken authenticates and bumps last_used_at. Materialize the
// candidate list before any follow-up Exec -- MaxOpenConns=1 will
// deadlock if a cursor is still open.
func (s *Store) LookupToken(raw string, now time.Time) (*Token, error) {
	if len(raw) < PrefixLen {
		return nil, ErrInvalidToken
	}
	prefix := raw[:PrefixLen]

	candidates, err := s.selectTokensByPrefix(prefix)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, ErrNoTokenCandidates
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
			return nil, ErrTokenRevoked
		}
		_, _ = s.execNoCtx(
			`UPDATE tokens SET last_used_at = ? WHERE hash = ?`,
			now.UTC().Unix(), t.Hash,
		)
		ts := now.UTC()
		t.LastUsedAt = &ts
		return t, nil
	}
	return nil, ErrUnknownToken
}

const selectTokensByPrefixSQL = `
        SELECT hash, prefix, principal, kind, scopes,
               created_at, expires_at, last_used_at, revoked_at,
               COALESCE(replaced_by, '')
          FROM tokens
         WHERE prefix = ?`

func (s *Store) selectTokensByPrefix(prefix string) ([]Token, error) {
	rows, err := s.queryNoCtx(selectTokensByPrefixSQL, prefix)
	if err != nil {
		return nil, fmt.Errorf("tokens: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTokenRows(rows)
}

func selectTokensByPrefixTx(ctx context.Context, tx *storeTx, prefix string) ([]Token, error) {
	rows, err := tx.QueryContext(ctx, selectTokensByPrefixSQL+tx.forUpdate(), prefix)
	if err != nil {
		return nil, fmt.Errorf("tokens: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTokenRows(rows)
}

func scanTokenRows(rows *sql.Rows) ([]Token, error) {
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

// RevokeToken sets revoked_at=now; row is kept for audit. A token
// already carrying a future revoked_at from a rotation grace window is
// clamped down to now, so an operator can cut a leaked token short.
func (s *Store) RevokeToken(prefix string, now time.Time) error {
	ctx := context.Background()
	ts := now.UTC().Unix()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("tokens: revoke: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE tokens SET revoked_at = ? WHERE prefix = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		ts, prefix, ts,
	)
	if err != nil {
		return fmt.Errorf("tokens: revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("tokens: revoke: %w", err)
	}
	if n == 0 {
		return errors.New("token not found or already revoked")
	}
	// safety: an ambiguous prefix rolls back, so a stranger's token stays live
	if n > 1 {
		return fmt.Errorf("tokens: prefix %q matched %d rows, aborting", prefix, n)
	}
	return tx.Commit()
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

	rows, err := s.queryNoCtx(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanTokenRows(rows)
}

// LookupTokenByPrefix returns the one row carrying prefix. More than one
// match names no single principal, so it is an error rather than a guess.
func (s *Store) LookupTokenByPrefix(prefix string) (*Token, error) {
	rows, err := s.selectTokensByPrefix(prefix)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("token not found")
	}
	if len(rows) > 1 {
		return nil, fmt.Errorf("tokens: prefix %q matched %d rows, aborting", prefix, len(rows))
	}
	return &rows[0], nil
}

// RotateToken mints a peer and revokes the original at now+grace. The
// read of the original, the mint, and the revocation share one
// transaction, so a revoke that lands while the replacement is hashing
// wins and the rotation rolls back rather than reviving the token.
func (s *Store) RotateToken(prefix string, grace, ttl time.Duration, now time.Time) (raw string, newTok, oldTok *Token, err error) {
	ctx := context.Background()
	for attempt := 1; ; attempt++ {
		raw, newTok, oldTok, err = s.rotateToken(ctx, prefix, grace, ttl, now)
		if err == nil {
			return raw, newTok, oldTok, nil
		}
		if attempt < mintAttempts && isTokenPrefixCollision(err) {
			continue
		}
		return "", nil, nil, err
	}
}

func (s *Store) rotateToken(
	ctx context.Context, prefix string, grace, ttl time.Duration, now time.Time,
) (string, *Token, *Token, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", nil, nil, fmt.Errorf("tokens: rotate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := selectTokensByPrefixTx(ctx, tx, prefix)
	if err != nil {
		return "", nil, nil, err
	}
	if len(rows) == 0 {
		return "", nil, nil, errors.New("token not found")
	}
	if len(rows) > 1 {
		return "", nil, nil, fmt.Errorf("tokens: prefix %q matched %d rows, aborting", prefix, len(rows))
	}
	oldTok := &rows[0]
	if oldTok.RevokedAt != nil {
		return "", nil, nil, errors.New("token is already revoked")
	}

	raw, newTok, err := createTokenRow(ctx, tx, oldTok.Principal, oldTok.Kind, oldTok.Scopes, ttl, now)
	if err != nil {
		return "", nil, nil, err
	}

	revokeAt := now.Add(grace).UTC()
	res, err := tx.ExecContext(ctx,
		`UPDATE tokens SET revoked_at = ?, replaced_by = ?
          WHERE prefix = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
		revokeAt.Unix(), newTok.Prefix, prefix, revokeAt.Unix(),
	)
	if err != nil {
		return "", nil, nil, fmt.Errorf("tokens: rotate update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", nil, nil, fmt.Errorf("tokens: rotate update: %w", err)
	}
	// safety: a revoke that landed since the read leaves no row to update, so the rotation rolls back
	if n == 0 {
		return "", nil, nil, errors.New("token is already revoked")
	}
	if n > 1 {
		return "", nil, nil, fmt.Errorf("tokens: prefix %q matched %d rows, aborting", prefix, n)
	}
	oldTok.RevokedAt = &revokeAt
	oldTok.ReplacedBy = newTok.Prefix
	if err := tx.Commit(); err != nil {
		return "", nil, nil, fmt.Errorf("tokens: rotate: %w", err)
	}
	return raw, newTok, oldTok, nil
}

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

func hashToken(raw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argonKey(raw, salt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("argon2id$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

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
	cand, err := argonKey(raw, salt)
	if err != nil {
		return false, err
	}
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
