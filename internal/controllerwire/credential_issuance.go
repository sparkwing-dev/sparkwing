package controllerwire

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// CreateTokenRequest narrows a member credential's grants and optional expiry.
type CreateTokenRequest struct {
	AllowTeamAdmin bool                  `json:"allow_team_admin,omitempty"`
	Grants         []string              `json:"grants"`
	ExpiresAt      *time.Time            `json:"expires_at,omitempty"`
	Data           *capability.DataScope `json:"data,omitempty"`
}

// CreatedToken contains the one-time bearer and its public issuance metadata.
type CreatedToken struct {
	Principal  string     `json:"principal,omitempty"`
	Capability string     `json:"capability"`
	Prefix     string     `json:"prefix"`
	Team       string     `json:"team"`
	Grants     []string   `json:"grants"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// EnrollRunnerRequest selects a machine name, repository patterns and authorized pools.
type EnrollRunnerRequest struct {
	Personal      bool       `json:"personal,omitempty"`
	Platform      string     `json:"platform,omitempty"`
	Name          string     `json:"name"`
	Repos         []string   `json:"repos,omitempty"`
	ReservedPools []string   `json:"reserved_pools,omitempty"`
	Trust         string     `json:"trust,omitempty"`
	Grants        []string   `json:"grants,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// CreateServiceTokenRequest selects deployment service grants without console authority.
type CreateServiceTokenRequest struct {
	Name      string     `json:"name"`
	Grants    []string   `json:"grants"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// RotatedToken contains the one-time replacement and the old credential's grace deadline.
type RotatedToken struct {
	Capability   string              `json:"capability"`
	New          capability.Metadata `json:"new"`
	OldPrefix    string              `json:"old_prefix"`
	OldRevokedAt time.Time           `json:"old_revoked_at"`
}
