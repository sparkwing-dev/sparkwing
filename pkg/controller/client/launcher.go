package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ClaimLaunch claims the next ready node of a controller-dispatched run for a
// launcher Job, with the claim token that Job's pod carries. It returns nil
// when no node is ready. deadline is the Job's life and the token's expiry.
func (c *Client) ClaimLaunch(ctx context.Context, holderID string, lease, deadline time.Duration) (*store.LaunchClaim, error) {
	buf, err := json.Marshal(map[string]any{
		"holder_id":     holderID,
		"lease_secs":    int(lease.Seconds()),
		"deadline_secs": int(deadline.Seconds()),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/launcher/claim", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var claim store.LaunchClaim
		if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
			return nil, err
		}
		return &claim, nil
	default:
		return nil, readHTTPError(resp)
	}
}
