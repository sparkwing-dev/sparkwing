package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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

// CreateToken mints a token. It needs the admin scope.
func (c *Client) CreateToken(ctx context.Context, req CreateTokenRequest) (*CreatedToken, error) {
	var out CreatedToken
	if err := c.post(ctx, "/api/v1/tokens", req, http.StatusCreated, &out); err != nil {
		return nil, err
	}
	return &out, nil
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
