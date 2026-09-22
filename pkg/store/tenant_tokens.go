package store

import (
	"context"
	"time"
)

// CreateToken mints a token belonging to t's team. It returns the RAW
// string only once; the hash is one-way.
func (t *Tenant) CreateToken(
	ctx context.Context, principal, kind string, scopes []string, ttl time.Duration, now time.Time,
) (string, *Token, error) {
	return t.CreateTokenWith(ctx, principal, kind, scopes, ttl, now, TokenOptions{})
}

// CreateTokenWith mints a token of t's team carrying opts. It is
// [Tenant.CreateToken] with the fields a plain mint leaves at their zero
// value, such as the metering marker an operator puts on a cloud runner's
// credential.
func (t *Tenant) CreateTokenWith(
	ctx context.Context, principal, kind string, scopes []string, ttl time.Duration, now time.Time, opts TokenOptions,
) (string, *Token, error) {
	for attempt := 1; ; attempt++ {
		raw, tok, err := createTokenRow(ctx, storeExecer{s: t.s}, t.team, principal, kind, scopes, ttl, now, opts)
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
