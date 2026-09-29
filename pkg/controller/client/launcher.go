package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// ErrControllerFailed marks an answer of 500 or more: the controller or
// something it called failed, and the same request may succeed again.
var ErrControllerFailed = errors.New("the controller failed to answer")

// SourceCredential asks for the one GitHub credential the calling claim's
// init container fetches runID's source with. The controller issues it once
// per claim, and never once the claim's attempt has started.
func (c *Client) SourceCredential(ctx context.Context, runID string) (*store.SourceCredential, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/runs/"+url.PathEscape(runID)+"/source-credential", http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, fmt.Errorf("%w: %w", ErrControllerFailed, readHTTPError(resp))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out store.SourceCredential
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
