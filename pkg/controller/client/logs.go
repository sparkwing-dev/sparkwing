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

// Append refuses: node logs reach the controller as segments.
func (NodeLogs) Append(context.Context, string, string, []byte) error {
	return fmt.Errorf("%w: node logs reach the controller as segments", storage.ErrNotSupported)
}

// DeleteRun refuses: deleting a run deletes its logs.
func (NodeLogs) DeleteRun(context.Context, string) error {
	return fmt.Errorf("%w: delete the run to delete its logs", storage.ErrNotSupported)
}

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

// GrepLine is a line of a node's log a [NodeLogs.Grep] matched, with
// its 1-based position in the whole log.
type GrepLine struct {
	LineNo int    `json:"line_no"`
	Line   string `json:"line"`
}

// Grep returns the lines of one node's log that contain pattern, with
// their line numbers; maxMatches above zero stops after that many.
func (l NodeLogs) Grep(ctx context.Context, runID, nodeID, pattern string, maxMatches int) ([]GrepLine, error) {
	q := url.Values{"grep": {pattern}, "line_numbers": {"1"}}
	if maxMatches > 0 {
		q.Set("max_matches", strconv.Itoa(maxMatches))
	}
	raw, err := l.getRaw(ctx, l.logPath(runID, nodeID), q)
	if err != nil {
		return nil, err
	}
	var out []GrepLine
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var line GrepLine
		if err := dec.Decode(&line); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode grep line: %w", err)
		}
		out = append(out, line)
	}
}

// Stream opens a node's log as server-sent events, each data line one
// raw log record, and returns (nil, nil) when the controller has no live
// read of the node.
func (l NodeLogs) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		l.c.baseURL+l.logPath(runID, nodeID)+"/stream?format=raw", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
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
