package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// CreateTokenRequest is the body of POST /api/v1/tokens. TTLSecs of zero mints
// a token that never expires.
type CreateTokenRequest struct {
	Kind      string   `json:"kind"`
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	TTLSecs   int64    `json:"ttl_secs,omitempty"`
}

// CreatedToken is a newly minted token. Token is the raw bearer, returned only
// this once; Metadata is the controller's record of it, as sent.
type CreatedToken struct {
	Token    string          `json:"token"`
	Metadata json.RawMessage `json:"metadata"`
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

// CreateToken mints a token. It needs the admin scope.
func (c *Client) CreateToken(ctx context.Context, req CreateTokenRequest) (*CreatedToken, error) {
	var out CreatedToken
	if err := c.post(ctx, "/api/v1/tokens", req, http.StatusCreated, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTokens lists the controller's tokens, narrowed to one kind when kind is
// not empty. Revoked tokens are left out unless includeRevoked is set.
func (c *Client) ListTokens(ctx context.Context, kind string, includeRevoked bool) ([]TokenInfo, error) {
	q := url.Values{}
	if kind != "" {
		q.Set("kind", kind)
	}
	if includeRevoked {
		q.Set("include_revoked", "1")
	}
	u := c.baseURL + "/api/v1/tokens"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var out struct {
		Tokens []TokenInfo `json:"tokens"`
	}
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	return out.Tokens, nil
}

// LookupToken returns the controller's record of the token with prefix, as
// sent.
func (c *Client) LookupToken(ctx context.Context, prefix string) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.getJSON(ctx, c.baseURL+"/api/v1/tokens/"+url.PathEscape(prefix), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeToken revokes a deployment service or operator capability by prefix.
func (c *Client) RevokeToken(ctx context.Context, prefix string) error {
	return c.deleteJSON(ctx, "/api/v1/operator/tokens/"+url.PathEscape(prefix), http.StatusNoContent, nil)
}

// RotateToken replaces an owned capability once; a nil expiry preserves its original expiry.
func (c *Client) RotateToken(ctx context.Context, prefix string, grace time.Duration, expires *time.Time) (*RotatedToken, error) {
	body := struct {
		GraceSecs int64      `json:"grace_secs"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}{GraceSecs: int64(grace / time.Second), ExpiresAt: expires}
	var out RotatedToken
	if err := c.post(ctx, "/api/v1/tokens/"+url.PathEscape(prefix)+"/rotate", body, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ComputeLimits returns the deployment's compute ceilings document, as sent.
func (c *Client) ComputeLimits(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.getJSON(ctx, c.baseURL+"/api/v1/operator/limits", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetComputeLimits sets the named ceilings, where zero removes one, and
// returns the compute ceilings document as it now stands.
func (c *Client) SetComputeLimits(ctx context.Context, limits map[string]int64) (json.RawMessage, error) {
	body := struct {
		Limits map[string]int64 `json:"limits"`
	}{Limits: limits}
	var out json.RawMessage
	if err := c.put(ctx, "/api/v1/operator/limits", body, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return out, nil
}
