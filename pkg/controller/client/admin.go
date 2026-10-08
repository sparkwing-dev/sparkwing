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

// TokenInfo is one token as GET /api/v1/tokens lists it. Times are Unix
// seconds.
type TokenInfo struct {
	Prefix     string   `json:"prefix"`
	Kind       string   `json:"kind"`
	Principal  string   `json:"principal"`
	Scopes     []string `json:"scopes"`
	Metered    bool     `json:"metered,omitempty"`
	LastUsedAt *int64   `json:"last_used_at,omitempty"`
	RevokedAt  *int64   `json:"revoked_at,omitempty"`
}

// RotatedToken is the replacement POST /api/v1/tokens/{prefix}/rotate mints.
// The old token keeps authenticating until OldRevokedAt (Unix seconds).
type RotatedToken struct {
	Token         string          `json:"token"`
	New           json.RawMessage `json:"new"`
	OldRevokedAt  int64           `json:"old_revoked_at"`
	OldReplacedBy string          `json:"old_replaced_by"`
}

// User is one dashboard user as GET /api/v1/users lists it. Times are Unix
// seconds; LastLoginAt is nil for a user who has never signed in.
type User struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	CreatedAt   int64    `json:"created_at"`
	LastLoginAt *int64   `json:"last_login_at"`
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

// RevokeToken revokes the token with prefix at once.
func (c *Client) RevokeToken(ctx context.Context, prefix string) error {
	return c.deleteJSON(ctx, "/api/v1/tokens/"+url.PathEscape(prefix), http.StatusNoContent, nil)
}

// RotateToken mints a replacement for the token with prefix. The old token
// keeps working for grace; a zero ttl keeps the old token's remaining
// lifetime.
func (c *Client) RotateToken(ctx context.Context, prefix string, grace, ttl time.Duration) (*RotatedToken, error) {
	body := struct {
		GraceSecs int64 `json:"grace_secs"`
		TTLSecs   int64 `json:"ttl_secs,omitempty"`
	}{GraceSecs: int64(grace.Seconds())}
	if ttl > 0 {
		body.TTLSecs = int64(ttl.Seconds())
	}
	var out RotatedToken
	if err := c.post(ctx, "/api/v1/tokens/"+url.PathEscape(prefix)+"/rotate", body, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ComputeLimits returns the team's compute ceilings document, as sent.
func (c *Client) ComputeLimits(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.getJSON(ctx, c.baseURL+"/api/v1/compute-limits", &out); err != nil {
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
	if err := c.put(ctx, "/api/v1/compute-limits", body, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateUser adds a dashboard user. Empty scopes take the controller's
// default.
func (c *Client) CreateUser(ctx context.Context, name, password string, scopes []string) error {
	body := struct {
		Name     string   `json:"name"`
		Password string   `json:"password"`
		Scopes   []string `json:"scopes,omitempty"`
	}{Name: name, Password: password, Scopes: scopes}
	return c.post(ctx, "/api/v1/users", body, http.StatusCreated, nil)
}

// ListUsers lists the controller's dashboard users.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var out struct {
		Users []User `json:"users"`
	}
	if err := c.getJSON(ctx, c.baseURL+"/api/v1/users", &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// DeleteUser removes a dashboard user.
func (c *Client) DeleteUser(ctx context.Context, name string) error {
	return c.deleteJSON(ctx, "/api/v1/users/"+url.PathEscape(name), http.StatusNoContent, nil)
}
