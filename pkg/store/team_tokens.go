package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// TokenFamily names which of two disjoint scope sets a token minted from
// team settings draws on. The family decides who may mint the token, how
// long it lives and how many a team or member may hold.
type TokenFamily string

const (
	// TokenFamilyMember acts as the member who minted it from a terminal or
	// script, with at most that member's scopes. It replaced the CLI token.
	TokenFamilyMember TokenFamily = "member"
	// TokenFamilyMachine authenticates one of the team's machines as the
	// agent it names. It replaced the runner token.
	TokenFamilyMachine TokenFamily = "machine"
)

var machineTokenScopes = []string{ScopeNodesClaim, ScopeTriggersClaim, ScopeRunsState, ScopeLogsWrite}

// MachineTokenScopes returns the scopes a machine team token may carry.
func MachineTokenScopes() []string { return slices.Clone(machineTokenScopes) }

// TeamTokenFamily reports the family a team token carrying scopes belongs
// to. The list must be non-empty, every scope must belong to one family, and
// a machine token must claim work of some kind; anything else is
// [ErrInvalidInput].
func TeamTokenFamily(scopes []string) (TokenFamily, error) {
	if len(scopes) == 0 {
		return "", fmt.Errorf("%w: a team token names at least one scope", ErrInvalidInput)
	}
	member := CLITokenScopes(RoleOwner)
	var family TokenFamily
	for _, scope := range scopes {
		var f TokenFamily
		switch {
		case slices.Contains(machineTokenScopes, scope):
			f = TokenFamilyMachine
		case slices.Contains(member, scope):
			f = TokenFamilyMember
		default:
			return "", fmt.Errorf("%w: %q is not a scope a team token carries", ErrInvalidInput, scope)
		}
		if family != "" && family != f {
			return "", fmt.Errorf("%w: a team token carries member scopes or machine scopes, not both", ErrInvalidInput)
		}
		family = f
	}
	if family == TokenFamilyMachine && !slices.Contains(scopes, ScopeNodesClaim) && !slices.Contains(scopes, ScopeTriggersClaim) {
		return "", fmt.Errorf("%w: a machine token carries %s or %s", ErrInvalidInput, ScopeNodesClaim, ScopeTriggersClaim)
	}
	return family, nil
}

// ActingKind reports the kind a credential acts as. A team token acts as the
// kind its family replaced, a machine token as a runner token and a member
// token as a user token, so every rule written for those kinds holds for it.
// Any other kind acts as itself.
func ActingKind(kind, principal string) string {
	if kind != TokenKindTeam {
		return kind
	}
	if strings.HasPrefix(principal, agentPrincipalPrefix) {
		return TokenKindRunner
	}
	return TokenKindUser
}

// Family reports the family of a token minted from team settings: machine
// for a runner token or a machine team token, member otherwise.
func (t *Token) Family() TokenFamily {
	if ActingKind(t.Kind, t.Principal) == TokenKindRunner {
		return TokenFamilyMachine
	}
	return TokenFamilyMember
}

func isAgentToken(kind, principal string) bool {
	return ActingKind(kind, principal) == TokenKindRunner && strings.HasPrefix(principal, agentPrincipalPrefix)
}

// MachinePrincipal is the principal a machine token named name carries.
func MachinePrincipal(name string) string { return agentPrincipalPrefix + name }

// safety: these must agree with [Token.Family], or a limit or listing would
// count a token under the family it does not act as.
const (
	machineTokenRow = `(kind = 'runner' OR (kind = 'team' AND substr(principal, 1, 6) = 'agent:'))`
	memberTokenRow  = `(kind = 'user' OR (kind = 'team' AND substr(principal, 1, 6) <> 'agent:'))`
)

func retryMint(mint func() (string, *Token, error)) (string, *Token, error) {
	for attempt := 1; ; attempt++ {
		raw, tok, err := mint()
		if err == nil {
			return raw, tok, nil
		}
		if attempt < mintAttempts && isTokenPrefixCollision(err) {
			continue
		}
		return "", nil, err
	}
}

// CreateMemberToken mints a member team token for accountID in t's team,
// filed under principal and carrying scopes. Every scope must be one the
// member's current role grants a CLI token, or the mint is [ErrRoleAboveOwn].
// The member holds at most [MaxCLITokensPerMember] live member tokens, CLI
// tokens included. It returns the raw token once. The token lives
// [CLITokenLifetime] and is revoked with the member's other tokens when they
// leave the team or are demoted to reader.
func (t *Tenant) CreateMemberToken(
	ctx context.Context, principal string, scopes []string, accountID string, now time.Time,
) (string, *Token, error) {
	if accountID == "" {
		return "", nil, fmt.Errorf("%w: a member token belongs to an account", ErrInvalidInput)
	}
	// safety: an agent: principal would make the token act as a machine.
	if principal == "" || strings.HasPrefix(principal, agentPrincipalPrefix) {
		return "", nil, fmt.Errorf("%w: a member token needs the member's principal", ErrInvalidInput)
	}
	if family, err := TeamTokenFamily(scopes); err != nil {
		return "", nil, err
	} else if family != TokenFamilyMember {
		return "", nil, fmt.Errorf("%w: a member token carries member scopes", ErrInvalidInput)
	}
	return retryMint(func() (string, *Token, error) {
		return t.createMemberTokenOnce(ctx, principal, scopes, accountID, now)
	})
}

func (t *Tenant) createMemberTokenOnce(
	ctx context.Context, principal string, scopes []string, accountID string, now time.Time,
) (string, *Token, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", nil, err
	}
	defer rollbackOrLog(tx)
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return "", nil, err
	}
	// safety: the membership lock also serializes demotion and removal, so a stale request cannot restore authority.
	role, err := t.roleTx(ctx, tx, accountID)
	if err != nil {
		return "", nil, err
	}
	allowed := CLITokenScopes(role)
	if slices.ContainsFunc(scopes, func(s string) bool { return !slices.Contains(allowed, s) }) {
		return "", nil, ErrRoleAboveOwn
	}
	at := now.UTC().Unix()
	var live int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tokens
		WHERE team = ? AND created_by = ? AND `+memberTokenRow+`
		  AND (revoked_at IS NULL OR revoked_at > ?) AND (expires_at IS NULL OR expires_at > ?)`,
		string(t.team), accountID, at, at).Scan(&live); err != nil {
		return "", nil, err
	}
	if live >= MaxCLITokensPerMember {
		return "", nil, ErrCLITokenLimit
	}
	raw, tok, err := createTokenRow(ctx, tx, t.team, principal, TokenKindTeam, scopes, CLITokenLifetime, now,
		TokenOptions{CreatedBy: accountID})
	if err != nil {
		return "", nil, err
	}
	return raw, tok, tx.Commit()
}

// CreateMachineToken mints a machine team token for the agent name in t's
// team, carrying scopes, on behalf of createdBy, who must be an editor or
// above. The team holds at most [MaxRunnerTokensPerTeam] live machine
// tokens, runner tokens included, and no two live ones share a name
// ([ErrAgentNameTaken]). It returns the raw token once. The token lives
// [RunnerTokenLifetime] past its latest use.
func (t *Tenant) CreateMachineToken(
	ctx context.Context, name string, scopes []string, createdBy string, now time.Time,
) (string, *Token, error) {
	if name == "" {
		return "", nil, fmt.Errorf("%w: a machine token needs a name", ErrInvalidInput)
	}
	if family, err := TeamTokenFamily(scopes); err != nil {
		return "", nil, err
	} else if family != TokenFamilyMachine {
		return "", nil, fmt.Errorf("%w: a machine token carries machine scopes", ErrInvalidInput)
	}
	return retryMint(func() (string, *Token, error) {
		return t.createMachineTokenOnce(ctx, MachinePrincipal(name), scopes, createdBy, now)
	})
}

func (t *Tenant) createMachineTokenOnce(
	ctx context.Context, principal string, scopes []string, createdBy string, now time.Time,
) (string, *Token, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", nil, err
	}
	defer rollbackOrLog(tx)
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return "", nil, err
	}
	// safety: the caller's role check ran before this transaction, and a
	// removal or demotion committed since then has already swept the tokens
	// this account minted, so a machine token minted now would outlive it.
	role, err := t.roleTx(ctx, tx, createdBy)
	if err != nil {
		return "", nil, err
	}
	if !role.AtLeast(RoleEditor) {
		return "", nil, ErrRoleAboveOwn
	}
	at := now.UTC().Unix()
	var live int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tokens
		WHERE team = ? AND `+machineTokenRow+`
		  AND (revoked_at IS NULL OR revoked_at > ?) AND (expires_at IS NULL OR expires_at > ?)`,
		string(t.team), at, at).Scan(&live); err != nil {
		return "", nil, err
	}
	if live >= MaxRunnerTokensPerTeam {
		return "", nil, ErrRunnerTokenLimit
	}
	raw, tok, err := createTokenRow(ctx, tx, t.team, principal, TokenKindTeam, scopes, RunnerTokenLifetime, now,
		TokenOptions{CreatedBy: createdBy})
	if err != nil {
		return "", nil, err
	}
	return raw, tok, tx.Commit()
}

const teamTokenColumns = `prefix, principal, kind, scopes, created_by, created_at, expires_at, last_used_at, revoked_at`

// TeamTokens lists the live tokens of t's team that accountID minted, of
// either family, and with allMachines every live machine token of the team
// too, oldest first.
func (t *Tenant) TeamTokens(ctx context.Context, accountID string, allMachines bool, now time.Time) (_ []Token, err error) {
	if accountID == "" {
		return nil, fmt.Errorf("%w: listing team tokens needs an account", ErrInvalidInput)
	}
	at := now.UTC().Unix()
	visible := `(created_by = ? AND kind IN ('user', 'runner', 'team'))`
	if allMachines {
		visible = `(` + visible + ` OR ` + machineTokenRow + `)`
	}
	rows, err := t.s.query(ctx, `SELECT `+teamTokenColumns+` FROM tokens
		WHERE team = ? AND `+visible+`
		  AND (revoked_at IS NULL OR revoked_at > ?) AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at, prefix`, string(t.team), accountID, at, at)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []Token
	for rows.Next() {
		tok, err := t.scanTeamToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	return out, rows.Err()
}

// TeamToken reads one token of t's team that a member could have minted from
// team settings, of either family, by prefix, revoked or not. Any other
// prefix is [ErrNotFound].
func (t *Tenant) TeamToken(ctx context.Context, prefix string) (Token, error) {
	tok, err := t.scanTeamToken(t.s.queryRow(ctx, `SELECT `+teamTokenColumns+` FROM tokens
		WHERE team = ? AND prefix = ? AND kind IN ('user', 'runner', 'team')`, string(t.team), prefix))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	return tok, err
}

func (t *Tenant) scanTeamToken(row interface{ Scan(...any) error }) (Token, error) {
	tok := Token{Team: t.team}
	var scopes string
	var created int64
	var expires, lastUsed, revoked sql.NullInt64
	if err := row.Scan(&tok.Prefix, &tok.Principal, &tok.Kind, &scopes, &tok.CreatedBy,
		&created, &expires, &lastUsed, &revoked); err != nil {
		return Token{}, err
	}
	tok.Scopes = splitScopes(scopes)
	tok.CreatedAt = time.Unix(created, 0).UTC()
	tok.ExpiresAt = unixPtr(expires)
	tok.LastUsedAt = unixPtr(lastUsed)
	tok.RevokedAt = unixPtr(revoked)
	return tok, nil
}

func unixPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	ts := time.Unix(v.Int64, 0).UTC()
	return &ts
}

// RevokeTeamToken revokes one token of t's team that [Tenant.TeamToken]
// reads. Deciding whether the caller may revoke it is the caller's job. A
// prefix of another team, of another kind, or already revoked is
// [ErrNotFound].
func (t *Tenant) RevokeTeamToken(ctx context.Context, prefix string, now time.Time) error {
	at := now.UTC().Unix()
	res, err := t.s.exec(ctx, `
		UPDATE tokens SET revoked_at = ?
		WHERE team = ? AND prefix = ? AND kind IN ('user', 'runner', 'team') AND (revoked_at IS NULL OR revoked_at > ?)`,
		at, string(t.team), prefix, at)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}
