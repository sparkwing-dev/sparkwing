package client

import (
	"context"
	"encoding/json"
	"net/http"
)

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
