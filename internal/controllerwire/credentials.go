package controllerwire

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// TokenMetadata is safe to list and persist. The issuer derives membership,
// metering, repository grants and reserved pools; no raw credential belongs here.
type TokenMetadata struct {
	Prefix        string              `json:"prefix"`
	Kind          string              `json:"kind"`
	Principal     string              `json:"principal"`
	Scopes        []capability.Grant  `json:"scopes"`
	Team          string              `json:"team,omitempty"`
	Subject       *capability.Subject `json:"subject,omitempty"`
	ReservedPools []string            `json:"reserved_pools,omitempty"`
	Repositories  []string            `json:"repositories,omitempty"`
	Metered       bool                `json:"metered,omitempty"`
	IssuedAt      *int64              `json:"issued_at,omitempty"`
	ExpiresAt     *int64              `json:"expires_at,omitempty"`
	LastUsedAt    *int64              `json:"last_used_at,omitempty"`
	RevokedAt     *int64              `json:"revoked_at,omitempty"`
}

type TokenQuery struct {
	Kind           string
	IncludeRevoked bool
}

type Tokens struct {
	Tokens []TokenMetadata `json:"tokens"`
}

// TokenCreate requests a root through the issuer's provisioning rules. TTL is
// seconds: nil and zero preserve the issuer's expiry policy.
type TokenCreate struct {
	AllowTeamAdmin bool               `json:"allow_team_admin,omitempty"`
	Kind           string             `json:"kind,omitempty"`
	Principal      string             `json:"principal,omitempty"`
	Scopes         []capability.Grant `json:"scopes,omitempty"`
	TTLSecs        *int64             `json:"ttl_secs,omitempty"`
	IdempotencyKey string             `json:"-"`
}

// TokenCreated returns the bearer once over the authenticated connection.
type TokenCreated struct {
	Token    string        `json:"token"`
	Metadata TokenMetadata `json:"metadata"`
}

// TokenRotate uses zero TTL to preserve the source token's remaining lifetime.
// Omission has the same effect; explicit negative lifetimes are invalid.
type TokenRotate struct {
	GraceSecs      int64  `json:"grace_secs"`
	TTLSecs        *int64 `json:"ttl_secs,omitempty"`
	IdempotencyKey string `json:"-"`
}

type TokenRotated struct {
	Token        string        `json:"token"`
	Metadata     TokenMetadata `json:"metadata"`
	OldPrefix    string        `json:"old_prefix"`
	OldRevokedAt int64         `json:"old_revoked_at"`
}

type TokenRevoked struct {
	Prefix  string `json:"prefix"`
	Revoked bool   `json:"revoked"`
}

// TokenManager binds all operations to either the team or deployment namespace.
// It cannot exchange a deployment token for operator-console powers.
type TokenManager interface {
	ListTokens(context.Context, TokenQuery) (Tokens, error)
	ReadToken(context.Context, string) (TokenMetadata, error)
	CreateToken(context.Context, TokenCreate) (TokenCreated, error)
	RotateToken(context.Context, string, TokenRotate) (TokenRotated, error)
	RevokeToken(context.Context, string) (TokenRevoked, error)
}
