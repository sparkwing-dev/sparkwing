package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// perf: an output is at most 64 MiB, and the node route's reply is bounded by
// the server's write timeout anyway.
const upstreamTimeout = 60 * time.Second

const maxUpstreamReply = 1 << 20

// Claim is one claimed node attempt's controller credential. The host holds
// it and forwards node routes with it; the node process never sees it. Make
// one per claim: it numbers the child runs the attempt starts, which the
// controller uses to hand a retried attempt the child its earlier one started.
type Claim struct {
	base  string
	token string
	http  *http.Client

	// safety: one call at a time, so a call that failed in transit keeps its
	// ordinal and its retry cannot start a second child.
	childMu   sync.Mutex
	nextChild int64
	mu        sync.Mutex
	children  map[string]bool
}

// NewClaim binds a claim token to the controller at controllerURL. Only a
// claim token is accepted, so a broader runner credential never backs a node.
func NewClaim(controllerURL, token string) (*Claim, error) {
	base, err := url.Parse(controllerURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, errors.New("node routes: the controller needs an absolute http(s) URL without userinfo")
	}
	if !strings.HasPrefix(token, store.ClaimTokenPrefix+"_") {
		return nil, errors.New("node routes: a claim token is required")
	}
	return &Claim{
		base: strings.TrimRight(base.String(), "/"), token: token, children: map[string]bool{},
		http: &http.Client{Timeout: upstreamTimeout, Transport: otelutil.WrapTransport(nil)},
	}, nil
}

var quote = strconv.Quote

var errUpstream = errors.New("controller unreachable")

// safety: a refusal comes back as its status and error code only; the
// controller's text never leaves this function.
func (c *Claim) call(ctx context.Context, method, path string, body, out any) (status int, code string, err error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", errUpstream
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamReply))
	if err != nil {
		return 0, "", errUpstream
	}
	if resp.StatusCode/100 != 2 {
		var refusal struct {
			Code string `json:"error"`
		}
		if json.Unmarshal(raw, &refusal) != nil {
			return resp.StatusCode, "", nil
		}
		return resp.StatusCode, refusal.Code, nil
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return 0, "", errUpstream
	}
	return resp.StatusCode, "", nil
}

func (c *Claim) fetchOutput(ctx context.Context, grant store.OutputReadGrant) (json.RawMessage, error) {
	if grant.URL == "" {
		return json.RawMessage("null"), nil
	}
	target := grant.URL
	if strings.HasPrefix(target, "/") {
		target = c.base + target
	}
	if u, err := url.Parse(target); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errUpstream
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errUpstream
	}
	// safety: a signed URL carries its own authority, so the claim token is
	// never sent with it.
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errUpstream
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errUpstream
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, store.MaxOutputBytes+1))
	if err != nil {
		return nil, errUpstream
	}
	// safety: the bytes must match the digest the controller recorded at
	// commit, so an object store serving anything else is refused.
	sum := sha256.Sum256(data)
	if int64(len(data)) != grant.Size || hex.EncodeToString(sum[:]) != grant.SHA256 || !json.Valid(data) {
		return nil, errUpstream
	}
	return data, nil
}

func (c *Claim) recordChild(id string) {
	c.mu.Lock()
	c.children[id] = true
	c.mu.Unlock()
}

func (c *Claim) startedChild(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.children[id]
}

func runPath(runID string, parts ...string) string {
	p := "/api/v1/runs/" + url.PathEscape(runID)
	for _, part := range parts {
		p += "/" + url.PathEscape(part)
	}
	return p
}

// safety: node ids are hierarchical, such as build/linux, so a slash separates
// parts while an empty, dot or dot-dot part, or a backslash, is refused.
func validNodeID(id string) bool {
	if id == "" || len(id) > 256 || strings.Contains(id, "\\") {
		return false
	}
	for part := range strings.SplitSeq(id, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validName(name string) bool {
	return name != "" && len(name) <= 256 && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}

func (s *Server) claimFor(w http.ResponseWriter, g *grant) (*Claim, bool) {
	if g.Claim == nil {
		s.writeError(w, http.StatusBadGateway, "upstream_unavailable", "this node has no controller to forward to")
		return nil, false
	}
	return g.Claim, true
}

// safety: a controller reply can quote URLs and credentials, so only the
// route and status are logged and the node gets a fixed message.
func (s *Server) upstreamFailed(w http.ResponseWriter, g *grant, route string, status int, err error) {
	attrs := []any{"run", g.RunID, "node", g.NodeID, "route", route, "status", status}
	if err != nil {
		attrs = append(attrs, "err_type", fmt.Sprintf("%T", err))
	}
	s.logger.Warn("node route forward failed", attrs...)
	s.writeError(w, http.StatusBadGateway, "upstream_unavailable", "the controller could not be reached; retry after a delay")
}

// safety: revoking the grant ends a forward in flight, not just the
// request's own context.
func forwardContext(r *http.Request, g *grant) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(g.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

func decodeBody(r *http.Request, into any) bool {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(into) == nil
}
