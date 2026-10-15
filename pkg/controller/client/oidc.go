package client

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// OIDCToken is an ID token the controller signed for one run.
type OIDCToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OIDCToken requests an ID token using a live run-bound oidc.mint capability.
func (c *Client) OIDCToken(ctx context.Context, runID, audience string) (*OIDCToken, error) {
	var out OIDCToken
	err := c.sendJSON(ctx, http.MethodPost, "/api/v1/runs/"+url.PathEscape(runID)+"/oidc-token",
		map[string]string{"audience": audience}, http.StatusOK, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
