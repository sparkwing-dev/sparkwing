package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// AppendNodeLiveLogAt posts bytes at an offset in a fenced execution attempt's log.
func (c *Client) AppendNodeLiveLogAt(ctx context.Context, runID, nodeID, attempt string, off int64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/logs", url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(store.LiveLogOffsetHeader, fmt.Sprint(off))
	req.Header.Set(store.LiveLogAttemptHeader, attempt)
	setNodeClaimFenceHeaders(req, ctx)
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return readHTTPError(resp)
	}
	return nil
}
