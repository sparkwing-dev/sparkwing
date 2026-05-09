package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/otelutil"
)

// Client is the HTTP client for the logs service. Workers use it to
// post lines; dashboards and CLIs use it to fetch.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client targeting the given logs-service URL.
// Nil httpClient uses a default with sensible defaults; callers who
// need connection-pooling tuning pass their own.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelutil.WrapTransport(nil),
		}
	}
	return &Client{baseURL: baseURL, http: httpClient}
}

// NewClientWithToken is NewClient plus a shared-secret bearer token.
// Every request carries `Authorization: Bearer <token>`. Empty token
// behaves like NewClient. When the caller supplies httpClient we
// trust their transport; when we build the default, the transport
// is otelhttp-wrapped so outgoing requests propagate trace context.
func NewClientWithToken(baseURL string, httpClient *http.Client, token string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelutil.WrapTransport(nil),
		}
	}
	if token != "" {
		base := httpClient.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		httpClient = &http.Client{
			Timeout:       httpClient.Timeout,
			CheckRedirect: httpClient.CheckRedirect,
			Jar:           httpClient.Jar,
			Transport:     &bearerTransport{base: base, token: token},
		}
	}
	return &Client{baseURL: baseURL, http: httpClient}
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// AuthError is the typed error Append returns on a 401 or 403 from
// the logs service. Callers (esp. the orchestrator's per-node log
// sink) treat this as fatal -- a token whose scope set is wrong is
// a deploy-time misconfig that won't fix itself by retrying, and
// silently dropping log lines under that misconfig produces a
// "status: success but no logs" black hole.
//
// Scope is parsed structured-first from the JSON body's
// "missing_scope" field. Older servers emit a plain
// string of the form "token lacks required scope: <name>"; the
// parser falls back to a regex on that exact phrasing.
// Empty when neither shape matches.
type AuthError struct {
	Status  int
	Scope   string
	RawBody string
}

func (e *AuthError) Error() string {
	if e.Scope != "" {
		return fmt.Sprintf("logs append blocked: token lacks scope %q (HTTP %d)", e.Scope, e.Status)
	}
	if e.RawBody != "" {
		return fmt.Sprintf("logs append blocked: HTTP %d: %s", e.Status, e.RawBody)
	}
	return fmt.Sprintf("logs append blocked: HTTP %d", e.Status)
}

// Append posts opaque bytes to a node's log. Typically called with
// one formatted line at a time, but the service accepts arbitrary
// payloads so clients can batch if they want to.
//
// 401 / 403 responses are returned as *AuthError so callers can
// distinguish auth misconfiguration (fatal) from transient
// transport errors (retryable). Other non-204 responses come back
// as a plain `logs append <code>: <body>` error.
func (c *Client) Append(ctx context.Context, runID, nodeID string, data []byte) error {
	u := fmt.Sprintf("%s/api/v1/logs/%s/%s", c.baseURL,
		url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	trimmed := string(bytes.TrimSpace(body))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &AuthError{
			Status:  resp.StatusCode,
			Scope:   parseMissingScope(trimmed),
			RawBody: trimmed,
		}
	}
	return fmt.Errorf("logs append %d: %s", resp.StatusCode, trimmed)
}

// parseMissingScope extracts the scope name from a 401/403 response
// body. Tries the JSON shape first (`missing_scope` field); falls
// back to the older plain-text phrasing
// "token lacks required scope: <name>" so mid-rollout we don't
// degrade against an older logs service / non-controller proxy.
// Returns "" when neither shape matches.
func parseMissingScope(body string) string {
	if body == "" {
		return ""
	}
	// JSON shape. Decode permissively: a body with
	// `missing_scope` but unexpected siblings still parses.
	if trimmed := strings.TrimLeft(body, " \t\n\r"); strings.HasPrefix(trimmed, "{") {
		var b AuthErrorBody
		if err := json.Unmarshal([]byte(body), &b); err == nil && b.MissingScope != "" {
			return b.MissingScope
		}
	}
	// Plain-text fallback.
	const marker = "token lacks required scope:"
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(body[i+len(marker):])
	// Stop at the first whitespace so trailing punctuation doesn't
	// leak into the scope name.
	if j := strings.IndexAny(rest, " \t\n\r,;"); j >= 0 {
		rest = rest[:j]
	}
	return strings.Trim(rest, ".\"'")
}

// Read fetches the current contents of a node's log as raw bytes.
// Empty (no log file yet) returns nil, nil -- callers treat that
// as "no content yet", not an error.
func (c *Client) Read(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return c.ReadFiltered(ctx, runID, nodeID, ReadFilter{})
}

// ReadFilter selects the subset of log lines the server returns.
// All filters are applied server-side so large logs never travel the
// wire. Zero values mean "no filter"; passing ReadFilter{} is
// equivalent to calling Read.
type ReadFilter struct {
	Tail  int    // last N lines; 0 disables
	Head  int    // first N lines; 0 disables
	Lines string // "A:B" inclusive 1-indexed range
	Grep  string // substring filter (case-sensitive)
}

// ReadFiltered is Read with server-side line filters. Matches the
// flag set exposed on `sparkwing jobs logs` so cluster-mode never
// tails a million lines over the wire.
func (c *Client) ReadFiltered(ctx context.Context, runID, nodeID string, f ReadFilter) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v1/logs/%s/%s", c.baseURL,
		url.PathEscape(runID), url.PathEscape(nodeID))
	if q := f.query(); q != "" {
		u += "?" + q
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("logs read %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return io.ReadAll(resp.Body)
}

func (f ReadFilter) query() string {
	q := url.Values{}
	if f.Tail > 0 {
		q.Set("tail", fmt.Sprintf("%d", f.Tail))
	}
	if f.Head > 0 {
		q.Set("head", fmt.Sprintf("%d", f.Head))
	}
	if f.Lines != "" {
		q.Set("lines", f.Lines)
	}
	if f.Grep != "" {
		q.Set("grep", f.Grep)
	}
	return q.Encode()
}

// Stream opens an SSE connection to the logs service and returns
// the raw response body. Callers read until EOF or context
// cancellation; close the returned body to terminate the stream
// early. Used by the web dashboard's live-tail proxy: bytes pass
// through verbatim so the browser's EventSource handles framing.
func (c *Client) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	u := fmt.Sprintf("%s/api/v1/logs/%s/%s/stream", c.baseURL,
		url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	// Defeat any client-side buffering; streams are long-lived.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("logs stream %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return resp.Body, nil
}

// DeleteRun removes every log file for the run from the service's
// storage. Idempotent (no error on missing run).
func (c *Client) DeleteRun(ctx context.Context, runID string) error {
	u := fmt.Sprintf("%s/api/v1/logs/%s", c.baseURL, url.PathEscape(runID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("logs delete %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

// ReadRun returns the concatenated logs for every node in a run.
// Banners delimit per-node sections.
func (c *Client) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v1/logs/%s", c.baseURL, url.PathEscape(runID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("logs read-run %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return io.ReadAll(resp.Body)
}
