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

// safety: a node holds at most this many forwards at once, so it bounds the
// host's handlers and buffers per node.
const forwardLimit = 4

// safety: awaits share one poller per child, so this bounds only the
// handlers a node holds open, not the controller's load.
const awaitLimit = 64

// Claim is one claimed node attempt's controller credential. The host holds
// it and forwards node routes with it; the node process never sees it. Make
// one per claim: it numbers the child runs the attempt starts, which the
// controller uses to hand a retried attempt the child its earlier one started.
type Claim struct {
	base  string
	token string
	http  *http.Client

	slots      chan struct{}
	awaitSlots chan struct{}
	// safety: one output in memory at a time, so a node holds at most one
	// 64 MiB output in the host however many reads it opens.
	outputSlot chan struct{}
	// safety: one call at a time, so a call that failed in transit keeps its
	// ordinal and its retry cannot start a second child.
	childSlot chan struct{}
	nextChild int64
	mu        sync.Mutex
	children  map[string]bool
	watches   map[string]*childWatch
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
		watches: map[string]*childWatch{}, awaitSlots: make(chan struct{}, awaitLimit),
		slots: make(chan struct{}, forwardLimit), outputSlot: make(chan struct{}, 1), childSlot: make(chan struct{}, 1),
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

// safety: the slot is taken before the body is read, and a node past its
// limit is refused at once, so waiting requests hold nothing; revoking the
// grant ends the forward too.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, g *grant) (*Claim, context.Context, func(), bool) {
	if g.Claim == nil {
		s.writeError(w, http.StatusBadGateway, "upstream_unavailable", "this node has no controller to forward to")
		return nil, nil, nil, false
	}
	return s.admit(w, r, g, g.Claim.slots, "controller requests")
}

func (s *Server) admit(w http.ResponseWriter, r *http.Request, g *grant, slots chan struct{}, what string) (*Claim, context.Context, func(), bool) {
	select {
	case slots <- struct{}{}:
	default:
		s.writeError(w, http.StatusTooManyRequests, "too_many_requests",
			fmt.Sprintf("a node has at most %d %s open at once; retry after one ends", cap(slots), what))
		return nil, nil, nil, false
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(g.ctx, cancel)
	return g.Claim, ctx, func() { stop(); cancel(); <-slots }, true
}

func acquire(ctx context.Context, slot chan struct{}) bool {
	select {
	case slot <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) writeOutput(ctx context.Context, w http.ResponseWriter, g *grant, route string, grant store.OutputReadGrant, reply map[string]any) {
	if !acquire(ctx, g.Claim.outputSlot) {
		s.upstreamFailed(w, g, route, 0, ctx.Err())
		return
	}
	defer func() { <-g.Claim.outputSlot }()
	output, err := g.Claim.fetchOutput(ctx, grant)
	if err != nil {
		s.upstreamFailed(w, g, route, 0, err)
		return
	}
	reply["output"] = output
	s.writeJSON(w, http.StatusOK, reply)
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

func decodeBody(r *http.Request, limit int64, into any) bool {
	return json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(into) == nil
}
