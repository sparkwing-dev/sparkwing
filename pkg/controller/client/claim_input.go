package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ClaimInput reads output through the calling claim's declared input reference.
func (c *Client) ClaimInput(ctx context.Context, runID, nodeID string, req store.ClaimInputRequest) (store.ClaimInput, []byte, error) {
	in, err := c.claimInput(ctx, runID, nodeID, req)
	if err != nil {
		return in, nil, err
	}
	data, err := c.fetchOutput(ctx, in.Output)
	return in, data, err
}

func (c *Client) claimInput(ctx context.Context, runID, nodeID string, req store.ClaimInputRequest) (store.ClaimInput, error) {
	var out store.ClaimInput
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+claimNodePath(runID, nodeID, "claim/input"), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.do(hreq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return out, json.NewDecoder(resp.Body).Decode(&out)
	case http.StatusNotFound:
		return out, notFound(resp)
	default:
		return out, readHTTPError(resp)
	}
}
