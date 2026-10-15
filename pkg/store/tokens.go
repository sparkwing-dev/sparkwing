package store

import (
	"context"
	"crypto/rand"
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
	TokenKindTeam    = "team"
)

// Token prefix markers; raw is `<prefix>_<entropy>`.
const (
	TokenPrefixUser    = "swu"
	TokenPrefixRunner  = "swr"
	TokenPrefixService = "sws"
	TokenPrefixTeam    = "swt"
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

var (
	ErrTokenRevoked = errors.New("token is revoked")
	ErrTokenExpired = errors.New("token is expired")
)

// Token is one row in the tokens table.
type Token struct {
	Hash   string
	Prefix string
	// Team owns the credential. A bearer lookup resolves the team a
	// request acts for from this, so a token that does not carry its own
	// team can only ever act for [DefaultTeam].
	Team       Team
	Principal  string
	Kind       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	ReplacedBy string // non-empty when this token has been rotated
	// Metered marks a token the operator mints for a runner credits pay
	// for. Only the operator sets it; a runner's own labels never do.
	Metered bool
	// CreatedBy names the account that minted the token from a team's
	// settings, which lets that account revoke it. Empty for a token an
	// operator minted.
	CreatedBy string
}

// TokenOptions carries the fields a mint sets beyond the required ones.
type TokenOptions struct {
	// Metered marks the minted token as one whose claims cost credits.
	Metered bool
	// CreatedBy names the account minting the token.
	CreatedBy string
}

// IsValid reports whether the token is usable at `now`.
func (t *Token) IsValid(now time.Time) bool {
	return TokenEnded(t.RevokedAt, t.ExpiresAt, now) == nil
}

// TokenEnded reports why a credential with these end times no longer
// authenticates at now: [ErrTokenRevoked], [ErrTokenExpired], or nil while it
// is live. Revocation wins when both have passed.
func TokenEnded(revokedAt, expiresAt *time.Time, now time.Time) error {
	if revokedAt != nil && !now.Before(*revokedAt) {
		return ErrTokenRevoked
	}
	if expiresAt != nil && !now.Before(*expiresAt) {
		return ErrTokenExpired
	}
	return nil
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
	case TokenPrefixTeam:
		return TokenKindTeam
	default:
		return ""
	}
}

// CreateToken mints a token. Returns the RAW string only once; the
// hash is one-way.
func (s *Store) CreateToken(principal, kind string, scopes []string, ttl time.Duration, now time.Time) (string, *Token, error) {
	return s.CreateTokenWith(context.Background(), principal, kind, scopes, ttl, now, TokenOptions{})
}

// CreateTokenWith mints a token carrying opts, under the caller's context.
// It is [Store.CreateToken] with the fields a plain mint leaves at their zero
// value, such as the metering marker an operator puts on a cloud runner's
// credential.
func (s *Store) CreateTokenWith(
	ctx context.Context, principal, kind string, scopes []string, ttl time.Duration, now time.Time, opts TokenOptions,
) (string, *Token, error) {
	for attempt := 1; ; attempt++ {
		// safety: the unscoped twin mints into [DefaultTeam], because a caller
		// predating the tenant key has no team to name and a credential filed
		// under any other team would authenticate nobody.
		raw, tok, err := createTokenRow(ctx, storeExecer{s: s}, DefaultTeam, principal, kind, scopes, ttl, now, opts)
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
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// MinSuppliedTokenLen is the minimum length of a supplied legacy token.
const MinSuppliedTokenLen = 32

// ValidateRawToken reports whether raw is shaped like a usable token of
// this kind: the kind's three-character marker, an underscore, and enough
// characters after it. A credential minted elsewhere has to carry that
// shape because the indexed prefix is what a bearer lookup selects on.
func ValidateRawToken(raw, kind string) error {
	marker, ok := prefixForKind(kind)
	if !ok {
		return fmt.Errorf("tokens: unknown kind %q", kind)
	}
	if TokenKindFromPrefix(raw) != kind {
		return fmt.Errorf("tokens: a %s token must start with %q", kind, marker+"_")
	}
	if len(raw) < MinSuppliedTokenLen {
		return fmt.Errorf("tokens: a %s token must be at least %d characters, got %d",
			kind, MinSuppliedTokenLen, len(raw))
	}
	return nil
}

func createTokenRow(
	ctx context.Context, e tokenExecer, team Team,
	principal, kind string, scopes []string, ttl time.Duration, now time.Time,
	opts TokenOptions,
) (string, *Token, error) {
	marker, ok := prefixForKind(kind)
	if !ok {
		return "", nil, fmt.Errorf("tokens: unknown kind %q", kind)
	}
	if err := retireExpiredAgentName(ctx, e, team, principal, kind, now); err != nil {
		return "", nil, err
	}
	if err := refuseTakenAgentName(ctx, e, team, principal, kind, now); err != nil {
		return "", nil, err
	}
	raw, err := mintRaw(marker)
	if err != nil {
		return "", nil, err
	}
	tok, err := insertTokenRow(ctx, e, raw, team, principal, kind, scopes, ttl, now, opts)
	if isAgentNameCollision(err) {
		return "", nil, fmt.Errorf("%w: %s", ErrAgentNameTaken, principal)
	}
	if err != nil {
		return "", nil, err
	}
	return raw, tok, nil
}

const agentNameIndexName = "idx_tokens_agent_name"

// safety: the index cannot read the clock, so it holds every unrevoked agent
// token; a rotation predecessor carries its grace-end revoked_at and falls
// outside it, while [refuseTakenAgentName] still counts it until that time.
const agentNameIndexedRow = `kind IN ('runner', 'team') AND revoked_at IS NULL AND substr(principal, 1, 6) = 'agent:'`

// safety: an expired agent token still holds its name's index slot, so a mint
// stamps its death as a revocation at the moment it expired, which leaves
// [Token.IsValid] answering exactly as before.
func retireExpiredAgentName(ctx context.Context, e tokenExecer, team Team, principal, kind string, now time.Time) error {
	if !isAgentToken(kind, principal) {
		return nil
	}
	_, err := e.ExecContext(ctx, `UPDATE tokens SET revoked_at = expires_at
		WHERE team = ? AND principal = ? AND `+agentNameIndexedRow+`
		  AND expires_at IS NOT NULL AND expires_at <= ?`,
		string(team), principal, now.UTC().Unix())
	return err
}

// hack: SQLite names the indexed columns in its error and Postgres names the index.
func isAgentNameCollision(err error) bool {
	if !isUniqueViolation(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, agentNameIndexName) || strings.Contains(msg, "tokens.team, tokens.principal")
}

// safety: every mint path passes through here, so a second live agent token
// for one agent name cannot be minted by any of them. Two racing mints both
// pass this read, and [agentNameIndex] refuses the second insert.
func refuseTakenAgentName(ctx context.Context, q tokenExecer, team Team, principal, kind string, now time.Time) (err error) {
	if !isAgentToken(kind, principal) {
		return nil
	}
	at := now.UTC().Unix()
	rows, err := q.QueryContext(ctx, `SELECT prefix FROM tokens
		WHERE team = ? AND kind IN (?, ?) AND principal = ?
		  AND (revoked_at IS NULL OR revoked_at > ?) AND (expires_at IS NULL OR expires_at > ?)`,
		string(team), TokenKindRunner, TokenKindTeam, principal, at, at)
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	if rows.Next() {
		return fmt.Errorf("%w: %s", ErrAgentNameTaken, principal)
	}
	return rows.Err()
}

func insertTokenRow(
	ctx context.Context, e tokenExecer, raw string, team Team,
	principal, kind string, scopes []string, ttl time.Duration, now time.Time,
	opts TokenOptions,
) (*Token, error) {
	if team == "" {
		return nil, ErrNoTeam
	}
	if principal == "" {
		return nil, errors.New("tokens: principal is required")
	}
	if err := ValidateRawToken(raw, kind); err != nil {
		return nil, err
	}
	var expires *time.Time
	if ttl > 0 {
		t := now.Add(ttl)
		expires = &t
	}
	hash, err := hashToken(raw)
	if err != nil {
		return nil, err
	}
	scoped := dedupeScopes(scopes)
	metered := 0
	if opts.Metered {
		metered = 1
	}
	if _, err := e.ExecContext(ctx, `
        INSERT INTO tokens (team, hash, prefix, principal, kind, scopes, created_at, expires_at, metered, created_by)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		string(team), hash, raw[:PrefixLen], principal, kind, strings.Join(scoped, ","),
		now.UTC().Unix(),
		expiresUnix(expires),
		metered,
		opts.CreatedBy,
	); err != nil {
		return nil, fmt.Errorf("tokens: insert: %w", err)
	}
	return &Token{
		Hash:      hash,
		Prefix:    raw[:PrefixLen],
		Team:      team,
		Principal: principal,
		Kind:      kind,
		Scopes:    scoped,
		CreatedAt: now.UTC(),
		ExpiresAt: expires,
		Metered:   opts.Metered,
		CreatedBy: opts.CreatedBy,
	}, nil
}

func isTokenPrefixCollision(err error) bool {
	return isUniqueViolation(err) &&
		strings.Contains(strings.ToLower(err.Error()), tokenPrefixColumn)
}

func prefixForKind(kind string) (string, bool) {
	switch kind {
	case TokenKindUser:
		return TokenPrefixUser, true
	case TokenKindRunner:
		return TokenPrefixRunner, true
	case TokenKindService:
		return TokenPrefixService, true
	case TokenKindTeam:
		return TokenPrefixTeam, true
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

var machineTokenScopes = []string{ScopeNodesClaim, ScopeTriggersClaim, ScopeRunsState}

// MachineTokenScopes returns the scopes a machine team token may carry.
func MachineTokenScopes() []string { return slices.Clone(machineTokenScopes) }

const agentPrincipalPrefix = "agent:"

// ActingKind resolves a legacy team credential's member or runner kind.
func ActingKind(kind, principal string) string {
	if kind != TokenKindTeam {
		return kind
	}
	if strings.HasPrefix(principal, agentPrincipalPrefix) {
		return TokenKindRunner
	}
	return TokenKindUser
}

func isAgentToken(kind, principal string) bool {
	return ActingKind(kind, principal) == TokenKindRunner && strings.HasPrefix(principal, agentPrincipalPrefix)
}
