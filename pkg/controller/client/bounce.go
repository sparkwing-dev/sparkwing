package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// PendingNodeBounce returns the oldest unconsumed bounce request for a
// node, or (nil, nil) when there is none. A runner calls it on every
// supervision tick, so "none" is the ordinary answer: the controller
// answers 204 and this reports no error.
func (c *Client) PendingNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/bounce",
		c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out store.NodeBounce
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConsumeNodeBounce closes a request with the outcome the runner
// produced: store.BounceBounced when the kill landed and the node was
// re-run, store.BounceMissed when the node finished first.
func (c *Client) ConsumeNodeBounce(ctx context.Context, runID, nodeID string, seq int64, outcome string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/bounce/consume",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]any{"seq": seq, "outcome": outcome},
		http.StatusNoContent, nil)
}
