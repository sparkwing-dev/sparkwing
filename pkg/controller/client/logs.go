package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

// NodeLogs is a read-only [storage.LogStore] over the controller's log
// routes. The controller serves a node's log from its segments, so a
// reader needs no other address:
//
//	data, err := c.NodeLogs().Read(ctx, runID, nodeID, storage.ReadOpts{Tail: 100})
type NodeLogs struct{ c *Client }

var _ storage.LogStore = NodeLogs{}

// NodeLogs returns the reader for this controller's node logs.
func (c *Client) NodeLogs() NodeLogs { return NodeLogs{c: c} }

func (l NodeLogs) logPath(runID, nodeID string) string {
	p := "/api/v1/runs/" + url.PathEscape(runID) + "/logs"
	if nodeID != "" {
		p += "/" + url.PathEscape(nodeID)
	}
	return p
}

func (l NodeLogs) getRaw(ctx context.Context, path string, q url.Values) ([]byte, error) {
	q.Set("format", "raw")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.c.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	return io.ReadAll(resp.Body)
}

// Read returns one node's log narrowed by opts; the controller applies
// the filters.
func (l NodeLogs) Read(ctx context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	q := url.Values{}
	if opts.Tail > 0 {
		q.Set("tail", strconv.Itoa(opts.Tail))
	}
	if opts.Head > 0 {
		q.Set("head", strconv.Itoa(opts.Head))
	}
	if opts.Lines != "" {
		q.Set("lines", opts.Lines)
	}
	if opts.Grep != "" {
		q.Set("grep", opts.Grep)
	}
	return l.getRaw(ctx, l.logPath(runID, nodeID), q)
}

// ReadRun returns every node's log of a run, each under a banner.
func (l NodeLogs) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	return l.getRaw(ctx, l.logPath(runID, ""), url.Values{})
}

// ErrLogStreamRefused reports a log stream the controller refused for
// good, such as one the token may not read, as against a failure a
// reconnect may get past.
var ErrLogStreamRefused = errors.New("controller/client: the controller refused the log stream")

// Stream opens a node's log as server-sent events, each data line one
// raw log record, resuming after the cursor [storage.WithLogCursor] put
// on ctx. It returns (nil, nil) when the controller has no live read of
// the node, and an error wrapping [ErrLogStreamRefused] for a 4xx other
// than a timeout or rate limit.
func (l NodeLogs) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		l.c.baseURL+l.logPath(runID, nodeID)+"/stream?format=raw", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if cursor := storage.LogCursor(ctx); cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	l.c.setRunnerIdentity(req)
	if l.c.token != "" {
		req.Header.Set("Authorization", "Bearer "+l.c.token)
	}
	streamer := *l.c.http
	streamer.Timeout = 0
	resp, err := streamer.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotImplemented {
		closeDrained(resp.Body)
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		err := readHTTPError(resp)
		closeDrained(resp.Body)
		if resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return nil, fmt.Errorf("%w: %w", ErrLogStreamRefused, err)
		}
		return nil, err
	}
	return resp.Body, nil
}

// NodeLogCompleteness asks the controller whether a node's log is
// whole; found is false when the controller cannot tell.
func (l NodeLogs) NodeLogCompleteness(ctx context.Context, runID, nodeID string, _ logseg.NodeProgress, _ time.Time) (logseg.Completeness, bool, error) {
	var c logseg.Completeness
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.c.baseURL+l.logPath(runID, nodeID)+"/completeness", nil)
	if err != nil {
		return c, false, err
	}
	resp, err := l.c.do(req)
	if err != nil {
		return c, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return c, false, readHTTPError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return c, false, fmt.Errorf("decode log completeness: %w", err)
	}
	return c, c.State != logseg.StateUnknown, nil
}
