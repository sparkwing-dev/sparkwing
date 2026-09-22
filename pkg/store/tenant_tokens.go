package store

import (
	"context"
	"errors"
	"slices"
	"time"
)

// OperatorScope is the deployment operator's scope. Only a token of
// [DefaultTeam], the operator's own, may carry it.
const OperatorScope = "admin"

// ErrOperatorScopeInTeam refuses a token of a signed-up team that asks for
// [OperatorScope]. Such a token would reach every team's deployment settings
// and skip the checks that are exempt for the operator, while acting as one
// team.
var ErrOperatorScopeInTeam = errors.New("store: a team's token cannot carry the operator scope")

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
	if t.team != DefaultTeam && slices.Contains(scopes, OperatorScope) {
		return "", nil, ErrOperatorScopeInTeam
	}
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
