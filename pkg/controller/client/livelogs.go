package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ErrNoLiveLog reports that the controller holds no live buffer for the
// node: it never wrote, or it finished and its buffer was released. The
// caller reads the durable copy instead. Every other failure is a real
// error and is returned as one.
var ErrNoLiveLog = errors.New("controller holds no live log for this node")

// LiveLogChunk is one live read of a running node's log: the bytes
// after the caller's offset, where to ask from next, and whether the
// node has finished writing.
type LiveLogChunk struct {
	// Start is the offset Data begins at. It exceeds the requested
	// offset when the controller's ring had already evicted those
	// bytes.
	Start int64 `json:"start"`

	// Next is the offset to pass to the next read.
	Next int64 `json:"next"`

	// Data is the log text between Start and Next.
	Data string `json:"data"`

	// Done reports that the node has finished writing.
	Done bool `json:"done"`
}

// AppendNodeLiveLog posts one batch of a running node's log lines to
// the controller, which holds them in a bounded ring for live readers.
// The durable copy is the logs surface's job; this call only feeds the
// live view. The node's claim travels in ctx, exactly as it does for
// every other node write.
func (c *Client) AppendNodeLiveLog(ctx context.Context, runID, nodeID string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/logs",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.postRaw(ctx, path, data, http.StatusNoContent)
}

// ReadNodeLiveLog returns the live log bytes the controller holds
// after since. A node with no live ring answers a 404, which reads as
// "watch the durable copy instead".
func (c *Client) ReadNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (*LiveLogChunk, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/logs?since=%d", c.baseURL,
		url.PathEscape(runID), url.PathEscape(nodeID), since)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoLiveLog
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out LiveLogChunk
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StreamNodeLiveLog opens the controller's server-sent-events stream
// of one running node's log from since, and hands back the raw body so
// the caller frames it. A node with no live ring answers a 404.
func (c *Client) StreamNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (io.ReadCloser, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/logs/stream?since=%d", c.baseURL,
		url.PathEscape(runID), url.PathEscape(nodeID), since)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	client := *c.http
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, ErrNoLiveLog
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("live log stream %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return resp.Body, nil
}
