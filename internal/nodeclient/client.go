// Package nodeclient calls the host's /node/v1 routes from a node-bound
// pipeline process. docs/node-protocol.md is the contract: the host passes
// the base URL and a bearer in SPARKWING_NODE_URL and SPARKWING_NODE_TOKEN,
// and the bearer confines every call to the process's own run and node.
package nodeclient

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
	"strings"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

// Codes the SDK acts on; docs/node-protocol.md lists every code a route
// answers.
const (
	CodeTooManyRequests     = "too_many_requests"
	CodeUpstreamUnavailable = "upstream_unavailable"
	CodeSecretNotFound      = "secret_not_found"
	CodeUnknownNode         = "unknown_node"
	CodeNotReady            = "not_ready"
	CodeNoOutput            = "no_output"
	CodeNoRun               = "no_run"
	CodeOIDCUnavailable     = "oidc_unavailable"
)

// MaxAwait is the longest wait one await call may ask the host to hold.
const MaxAwait = 60 * time.Second

const (
	// perf: the host bounds its own controller forward at 60 s, so this
	// only catches a host that stopped answering.
	requestTimeout = 90 * time.Second
	// safety: an output is at most 64 MiB; the reply's envelope adds little.
	maxReply      = 65 << 20
	maxRetryDelay = time.Second
	upstreamTries = 3
	// safety: a node whose slots stay full past this gets the 429, so a
	// call without a deadline still ends.
	retryBudget = 2 * time.Minute
)

// Error is a refusal from a node route: its status and the spec's code.
// Message is the host's text, which never carries the bearer.
type Error struct {
	Method  string
	Path    string
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("node route %s %s: %s (%d %s)", e.Method, e.Path, e.Message, e.Status, e.Code)
}

// HasCode reports whether err is a route refusal carrying code.
func HasCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Client calls one node's routes. It is safe for concurrent use.
type Client struct {
	base   string
	token  string
	http   *http.Client
	budget time.Duration
}

// New binds a client to the routes at baseURL with bearer. baseURL must be
// http on loopback with an explicit port: http://127.0.0.1:<port>,
// http://[::1]:<port> or http://localhost:<port>.
func New(baseURL, bearer string) (*Client, error) {
	if !loopback(baseURL) {
		// safety: the URL is not echoed, since a wrong one may carry userinfo.
		return nil, fmt.Errorf("%s must be http://127.0.0.1:<port>, http://[::1]:<port> or http://localhost:<port>; "+
			"the node routes are served on loopback only", protofd.NodeURLEnv)
	}
	if bearer == "" {
		return nil, fmt.Errorf("%s is empty", protofd.NodeTokenEnv)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// safety: the bearer goes to the host alone, never through a proxy.
	transport.Proxy = nil
	return &Client{
		base: strings.TrimRight(baseURL, "/"), token: bearer, budget: retryBudget,
		http: &http.Client{
			Transport: transport,
			// safety: a redirect could carry the bearer to another local
			// service, so none is followed and a 3xx is an error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host, port := u.Hostname(), u.Port()
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return false
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// FromProcess builds the client from the URL and bearer the host passed to
// this --sw-node-protocol process, which protofd took from the environment
// at initialization. It returns nil when the host passed neither.
func FromProcess() (*Client, error) {
	base, token := protofd.NodeRoutes()
	if base == "" && token == "" {
		return nil, nil
	}
	return New(base, token)
}

var (
	bound   atomic.Bool
	process atomic.Pointer[Client]
)

// Bind marks this process as node-protocol-bound, with c (nil when the host
// gave it no routes) as its client.
func Bind(c *Client) {
	process.Store(c)
	bound.Store(true)
}

// Bound returns the process's client and whether the process is bound.
func Bound() (*Client, bool) { return process.Load(), bound.Load() }

func (c *Client) do(ctx context.Context, method, path string, timeout time.Duration, body, out any, want int) error {
	return c.call(ctx, method, path, timeout, body, out, want, upstreamTries)
}

// safety: tries bounds only 502 retries; a 429 is refused before the host
// forwards anything, so retrying it never repeats a request, and the budget
// bounds it.
func (c *Client) call(ctx context.Context, method, path string, timeout time.Duration, body, out any, want, tries int) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	delay := 25 * time.Millisecond
	giveUp := time.Now().Add(c.budget)
	for try := 1; ; try++ {
		err := c.once(ctx, method, path, timeout, payload, out, want)
		var e *Error
		switch {
		case !errors.As(err, &e):
			return err
		case time.Now().Add(delay).After(giveUp):
			return err
		case e.Code == CodeTooManyRequests:
		case e.Code == CodeUpstreamUnavailable && try < tries:
		default:
			return err
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w: %w", ctx.Err(), err)
		case <-t.C:
		}
		delay = min(2*delay, maxRetryDelay)
	}
}

func (c *Client) once(ctx context.Context, method, path string, timeout time.Duration, payload []byte, out any, want int) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("node route %s %s: %w", method, path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("node route %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	switch {
	case err != nil:
		return fmt.Errorf("node route %s %s: read reply: %w", method, path, err)
	case len(raw) > maxReply:
		return fmt.Errorf("node route %s %s: reply exceeds %d bytes", method, path, maxReply)
	case resp.StatusCode != want:
		e := &Error{Method: method, Path: path, Status: resp.StatusCode}
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(raw, &body) == nil {
			e.Message, e.Code = body.Error, body.Code
		}
		if resp.StatusCode/100 == 3 {
			e.Message = "the node routes never redirect; refused to follow"
		}
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return e
	case out != nil:
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("node route %s %s: decode reply: %w", method, path, err)
		}
	}
	return nil
}

func escape(id string) string { return url.PathEscape(id) }

// Secret reads a secret the node's pipeline declares. masked reports that
// the host registered the value with the run's log masker.
func (c *Client) Secret(ctx context.Context, name string) (value string, masked bool, err error) {
	var out struct {
		Value  string `json:"value"`
		Masked bool   `json:"masked"`
	}
	err = c.do(ctx, http.MethodGet, "/node/v1/secrets/"+escape(name), requestTimeout, nil, &out, http.StatusOK)
	return out.Value, out.Masked, err
}

// NodeOutput reads the JSON output of a node of this run that this node
// depends on; JSON null when it produced none.
func (c *Client) NodeOutput(ctx context.Context, node string) (json.RawMessage, error) {
	var out struct {
		Output json.RawMessage `json:"output"`
	}
	err := c.do(ctx, http.MethodGet, "/node/v1/nodes/"+escape(node)+"/output", requestTimeout, nil, &out, http.StatusOK)
	return out.Output, err
}

// PipelineLatest reads node's output from the newest successful run of
// pipeline, finished within maxAge when it is positive.
func (c *Client) PipelineLatest(ctx context.Context, pipeline, node string, maxAge time.Duration) (runID string, output json.RawMessage, err error) {
	q := url.Values{"node": {node}}
	if maxAge > 0 {
		q.Set("max_age_ms", strconv.FormatInt(maxAge.Milliseconds(), 10))
	}
	var out struct {
		RunID  string          `json:"run_id"`
		Output json.RawMessage `json:"output"`
	}
	err = c.do(ctx, http.MethodGet, "/node/v1/pipelines/"+escape(pipeline)+"/latest?"+q.Encode(), requestTimeout, nil, &out, http.StatusOK)
	return out.RunID, out.Output, err
}

// OIDCToken asks the controller for an ID token for this run with aud set
// to audience. The host masks the token before it answers.
func (c *Client) OIDCToken(ctx context.Context, audience string) (token string, expiresAt time.Time, err error) {
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err = c.do(ctx, http.MethodPost, "/node/v1/oidc-token", requestTimeout, map[string]string{"audience": audience}, &out, http.StatusOK)
	return out.Token, out.ExpiresAt, err
}

// ChildRequest names the pipeline a child run starts.
type ChildRequest struct {
	Pipeline string            `json:"pipeline"`
	Args     map[string]string `json:"args,omitempty"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
}

// StartChild starts a child run of this node and returns its run id.
func (c *Client) StartChild(ctx context.Context, req ChildRequest) (string, error) {
	var out struct {
		RunID string `json:"run_id"`
	}
	// safety: a lost reply can leave a started child behind, and a retry
	// racing another call's would start a second, so a 502 is returned.
	err := c.call(ctx, http.MethodPost, "/node/v1/children", requestTimeout, req, &out, http.StatusCreated, 1)
	return out.RunID, err
}

// ChildStatus is one await's answer. Output is node's output once Status is
// success.
type ChildStatus struct {
	RunID  string          `json:"run_id"`
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}

// AwaitChild waits up to wait, at most MaxAwait, for a child run this node
// started to finish, and returns its status then. An empty node asks for no
// output.
func (c *Client) AwaitChild(ctx context.Context, run, node string, wait time.Duration) (ChildStatus, error) {
	wait = min(max(wait, 0), MaxAwait)
	q := url.Values{"wait_ms": {strconv.FormatInt(wait.Milliseconds(), 10)}}
	if node != "" {
		q.Set("node", node)
	}
	var out ChildStatus
	err := c.do(ctx, http.MethodGet, "/node/v1/children/"+escape(run)+"/await?"+q.Encode(), wait+requestTimeout, nil, &out, http.StatusOK)
	return out, err
}

// RegisterCleanup asks the host to run argv, with no shell, once this
// process exits, unless the cleanup is released first.
func (c *Client) RegisterCleanup(ctx context.Context, argv []string, description string) (string, error) {
	body := struct {
		Argv        []string `json:"argv"`
		Description string   `json:"description,omitempty"`
	}{argv, description}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/node/v1/cleanups", requestTimeout, body, &out, http.StatusCreated)
	return out.ID, err
}

// ReleaseCleanup drops a registered cleanup so the host never runs it.
func (c *Client) ReleaseCleanup(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/node/v1/cleanups/"+escape(id), requestTimeout, nil, nil, http.StatusNoContent)
}
