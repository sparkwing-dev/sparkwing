package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// WriteNodeDispatch POSTs a dispatch snapshot for the given run/node.
// Best-effort: caller treats a non-2xx as a no-op so a snapshot
// failure can't fail the node it describes.
func (c *Client) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	if d.RunID == "" || d.NodeID == "" {
		return fmt.Errorf("WriteNodeDispatch: run_id and node_id required")
	}
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/dispatch",
		url.PathEscape(d.RunID), url.PathEscape(d.NodeID))
	return c.post(ctx, path, d, http.StatusCreated, nil)
}

// GetNodeDispatch fetches the dispatch snapshot at the given seq. Pass
// seq < 0 to fetch the most-recent attempt. ErrNotFound when no row
// matches.
func (c *Client) GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/dispatch",
		c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
	if seq >= 0 {
		u += "?seq=" + strconv.Itoa(seq)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var d store.NodeDispatch
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
			return nil, err
		}
		return &d, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// ListNodeDispatches fetches every dispatch snapshot for (runID, nodeID),
// oldest seq first. Returns an empty slice (not ErrNotFound) when the
// node has no recorded dispatches.
func (c *Client) ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/dispatches",
		c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out []*store.NodeDispatch
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		return out, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}
