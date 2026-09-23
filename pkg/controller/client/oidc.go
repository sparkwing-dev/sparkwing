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

// OIDCToken asks the controller to sign an ID token for runID with the
// given audience. The caller must hold a live claim on the run; any other
// caller, and a controller with no signing key, gets a not-found error.
func (c *Client) OIDCToken(ctx context.Context, runID, audience string) (*OIDCToken, error) {
	var out OIDCToken
	err := c.sendJSON(ctx, http.MethodPost, "/api/v1/runs/"+url.PathEscape(runID)+"/oidc-token",
		map[string]string{"audience": audience}, http.StatusOK, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
