package client

import "time"

// CreateTokenRequest narrows a member credential's grants and optional expiry.
type CreateTokenRequest struct {
	Grants    []string   `json:"grants"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CreatedToken contains the one-time bearer and its public issuance metadata.
type CreatedToken struct {
	Capability string     `json:"capability"`
	Prefix     string     `json:"prefix"`
	Team       string     `json:"team"`
	Grants     []string   `json:"grants"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// EnrollRunnerRequest selects a machine name, repository patterns and authorized pools.
type EnrollRunnerRequest struct {
	Name          string     `json:"name"`
	Repos         []string   `json:"repos"`
	ReservedPools []string   `json:"reserved_pools,omitempty"`
	Grants        []string   `json:"grants,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// CreateServiceTokenRequest selects deployment service grants without console authority.
type CreateServiceTokenRequest struct {
	Name      string     `json:"name"`
	Grants    []string   `json:"grants"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// TokenSubject identifies the account, machine, service or operator holding a credential.
type TokenSubject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// TokenInfo is public capability metadata returned by the token resource.
type TokenInfo struct {
	Prefix         string       `json:"prefix"`
	Team           string       `json:"team"`
	Subject        TokenSubject `json:"subject"`
	Audience       string       `json:"audience"`
	Grants         []string     `json:"grants"`
	CreatedBy      string       `json:"created_by"`
	IssuedAt       time.Time    `json:"issued_at"`
	ExpiresAt      *time.Time   `json:"expires_at,omitempty"`
	LastUsedAt     *time.Time   `json:"last_used_at,omitempty"`
	RevokedAt      *time.Time   `json:"revoked_at,omitempty"`
	GitCredentials bool         `json:"git_credentials"`
	Metered        bool         `json:"metered"`
}

// RotatedToken contains the one-time replacement and the old credential's grace deadline.
type RotatedToken struct {
	Capability   string    `json:"capability"`
	New          TokenInfo `json:"new"`
	OldPrefix    string    `json:"old_prefix"`
	OldRevokedAt time.Time `json:"old_revoked_at"`
}
