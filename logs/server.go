// Package logs is the sparkwing-logs service: an HTTP frontend over
// file-per-node log storage. Workers POST log bytes as they stream;
// the dashboard and CLI fetch them for display.
//
// Why a separate service (not the controller)?
//
//   - Controller state is structured + small + queryable; logs are
//     unstructured + large + append-heavy. Different storage, different
//     access patterns.
//   - Logs scale with pipeline volume; controller DB shouldn't.
//   - In prod the logs service can back to S3 / gitcache / blob store
//     without touching the control plane.
//
// v1 is the simplest possible implementation: one file per
// (run_id, node_id) under `root/runs/<run_id>/<node_id>.log`, raw
// bytes appended on POST, whole file returned on GET. Fine for laptop
// iteration; clustered prod will grow this out.
package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/otelutil"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server handles HTTP requests against a filesystem-backed log store.
type Server struct {
	root   string
	logger *slog.Logger
	mu     sync.Mutex // guards concurrent opens of the same file
	// Auth is whoami-based: forward the incoming Authorization header
	// to the controller's /api/v1/auth/whoami endpoint, cache the
	// resolved principal, enforce per-route scope checks. Empty
	// controllerURL = auth off (laptop-local dev).
	controllerURL string
	authCache     sync.Map // map[string]*logsAuthCacheEntry
	authCacheTTL  time.Duration
	authHTTP      *http.Client
}

// New constructs a Server rooted at dir (created if absent). A nil
// logger uses slog.Default.
func New(root string, logger *slog.Logger) (*Server, error) {
	if root == "" {
		return nil, errors.New("logs: root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("logs: mkdir %s: %w", root, err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{root: root, logger: logger}, nil
}

// WithControllerAuth wires the controller's /api/v1/auth/whoami
// endpoint as the authoritative lookup for incoming tokens. The
// logs-service forwards the incoming Authorization header to whoami
// and trusts the returned principal. Empty controllerURL = auth off
// (laptop-local dev).
//
// Caching is in-process with the given TTL; cacheTTL=0 disables the
// cache entirely. Tokens that fail to authenticate are NOT cached
// (avoids a cache-poisoning vector where a brief outage pins 401s).
func (s *Server) WithControllerAuth(controllerURL string, cacheTTL time.Duration) *Server {
	s.controllerURL = controllerURL
	s.authCacheTTL = cacheTTL
	s.authHTTP = &http.Client{Timeout: 5 * time.Second}
	return s
}

// Handler returns the routed HTTP handler.
//
// Auth shape:
//   - /api/v1/health and /metrics are always unauthenticated. The k8s
//     probe + Prometheus scrape need to reach them without an
//     Authorization header.
//   - Everything else goes through authMiddleware. `sw*_`-prefixed
//     tokens are resolved via the controller's /api/v1/auth/whoami
//     endpoint.
//   - Per-route scope checks (logs.read for GETs, logs.write for
//     POST/DELETE) enforce the principal's scope set. Admin is an
//     implicit superset.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Append bytes to a node's log file. Body is treated as opaque
	// text; no parsing or structure enforcement.
	mux.Handle("POST /api/v1/logs/{runID}/{nodeID}", s.requireScope(scopeLogsWrite, http.HandlerFunc(s.handleAppend)))
	// Read the current contents of a node's log.
	mux.Handle("GET /api/v1/logs/{runID}/{nodeID}", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleRead)))
	// Read every node's log for a run, concatenated with banners.
	mux.Handle("GET /api/v1/logs/{runID}", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleReadRun)))
	// Delete every log file for a run. Idempotent.
	mux.Handle("DELETE /api/v1/logs/{runID}", s.requireScope(scopeLogsWrite, http.HandlerFunc(s.handleDeleteRun)))
	// Live tail: SSE stream of appended bytes.
	mux.Handle("GET /api/v1/logs/{runID}/{nodeID}/stream", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleStream)))

	// Session G: full-text search across every run's log files.
	mux.Handle("GET /api/v1/logs/search", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleSearch)))

	// Wrap the authenticated routes with the auth middleware, then
	// compose with the unauthed health + metrics routes + request log.
	authed := s.authMiddleware(mux)

	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	router.Handle("GET /metrics", promhttp.Handler())
	router.Handle("/", authed)
	// otelhttp wraps outermost; see pkg/controller/server.go for the
	// same pattern rationale.
	return otelutil.WrapHandler("sparkwing-logs", withRequestLog(router, s.logger))
}

// Scope constants mirrored from pkg/controller to avoid importing it.
// Kept in sync manually; the set is small and changes rarely. A lint
// task or a shared package can land later.
const (
	scopeLogsRead  = "logs.read"
	scopeLogsWrite = "logs.write"
	scopeAdmin     = "admin"
)

type logsPrincipal struct {
	Name        string
	Kind        string
	Scopes      []string
	TokenPrefix string
}

func (p *logsPrincipal) hasScope(s string) bool {
	if p == nil {
		return false
	}
	for _, x := range p.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

type logsPrincipalCtxKey struct{}

func contextWithLogsPrincipal(ctx context.Context, p *logsPrincipal) context.Context {
	return context.WithValue(ctx, logsPrincipalCtxKey{}, p)
}

func logsPrincipalFromContext(ctx context.Context) (*logsPrincipal, bool) {
	p, ok := ctx.Value(logsPrincipalCtxKey{}).(*logsPrincipal)
	return p, ok
}

type logsAuthCacheEntry struct {
	principal *logsPrincipal
	expires   time.Time
}

// authDisabled reports whether both local legacy tokens AND controller
// whoami are unconfigured, in which case the middleware is
// pass-through.
func (s *Server) authDisabled() bool {
	return s.controllerURL == ""
}

// authMiddleware authenticates the caller and stamps a logsPrincipal
// on the request context. When auth is disabled, it's a pass-through.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	if s.authDisabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := extractBearer(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		p, err := s.authenticate(r.Context(), raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithLogsPrincipal(r.Context(), p)))
	})
}

// requireScope wraps a handler so only principals with the given
// scope (or admin) can reach it. Pass-through when auth is disabled.
func (s *Server) requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := logsPrincipalFromContext(r.Context())
		if !ok {
			// Auth disabled -- pass through.
			next.ServeHTTP(w, r)
			return
		}
		if p.hasScope(scopeAdmin) || p.hasScope(scope) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "token lacks required scope: "+scope, http.StatusForbidden)
	})
}

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix)), nil
}

// authenticate resolves a raw bearer token to a principal by calling
// the controller's /api/v1/auth/whoami endpoint. Results are cached
// per token for authCacheTTL to keep the per-request cost off the
// wire under normal load.
func (s *Server) authenticate(ctx context.Context, raw string) (*logsPrincipal, error) {
	if raw == "" {
		return nil, errors.New("missing bearer token")
	}

	// Cache hit.
	if s.authCacheTTL > 0 {
		if v, ok := s.authCache.Load(raw); ok {
			e := v.(*logsAuthCacheEntry)
			if time.Now().Before(e.expires) {
				return e.principal, nil
			}
			s.authCache.Delete(raw)
		}
	}

	if s.controllerURL == "" {
		return nil, errors.New("invalid bearer token")
	}
	p, err := s.whoami(ctx, raw)
	if err != nil {
		return nil, err
	}
	s.cacheAuth(raw, p)
	return p, nil
}

func (s *Server) cacheAuth(raw string, p *logsPrincipal) {
	if s.authCacheTTL <= 0 {
		return
	}
	s.authCache.Store(raw, &logsAuthCacheEntry{
		principal: p,
		expires:   time.Now().Add(s.authCacheTTL),
	})
}

// whoamiResp mirrors controller.whoamiResp. Kept as a local type to
// avoid importing the controller package.
type whoamiResp struct {
	Principal   string   `json:"principal"`
	Kind        string   `json:"kind"`
	Scopes      []string `json:"scopes"`
	TokenPrefix string   `json:"token_prefix"`
}

func (s *Server) whoami(ctx context.Context, rawToken string) (*logsPrincipal, error) {
	url := strings.TrimRight(s.controllerURL, "/") + "/api/v1/auth/whoami"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+rawToken)
	resp, err := s.authHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whoami: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whoami returned %d", resp.StatusCode)
	}
	var body whoamiResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("whoami decode: %w", err)
	}
	return &logsPrincipal{
		Name:        body.Principal,
		Kind:        body.Kind,
		Scopes:      body.Scopes,
		TokenPrefix: body.TokenPrefix,
	}, nil
}

// Serve starts the HTTP listener and blocks until ctx is done.
func Serve(ctx context.Context, root, addr string, logger *slog.Logger) error {
	return ServeWithTokens(ctx, root, addr, "", logger)
}

// ServeWithTokens starts the HTTP listener with whoami-based auth
// wired against the given controller URL. Empty controllerURL = auth
// fully disabled (laptop-local).
func ServeWithTokens(ctx context.Context, root, addr string, controllerURL string, logger *slog.Logger) error {
	s, err := New(root, logger)
	if err != nil {
		return err
	}
	if controllerURL != "" {
		s.WithControllerAuth(controllerURL, 60*time.Second)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute, // POST bodies can grow
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("logs service listening",
			"addr", addr, "root", root,
			"auth_controller", controllerURL != "",
		)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// --- handlers ---

// handleHealth reports log-service self-health as a degraded-list:
//
//	{"status": "ok" | "degraded", "problems": ["comp: detail", ...]}
//
// Checks: root dir writable + disk-free headroom. The goal isn't an
// exhaustive tripwire; it's "sparkwing health --on prod" surfacing
// the usual "my log volume filled up" symptom without an on-call
// having to kubectl-exec. Only a hard failure (root missing /
// unwritable) drops HTTP status to 503 -- disk-low degrades in-body.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	var problems []string

	// 1. Writability canary. Write + delete a small file under root.
	// A failure here flips to 503 -- log writes would fail too.
	canary := filepath.Join(s.root, ".health-check")
	if err := os.WriteFile(canary, []byte("ok"), 0o644); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status":"degraded","problems":["root: %s"]}`,
			strings.ReplaceAll(err.Error(), `"`, `\"`))
		return
	}
	_ = os.Remove(canary)

	// 2. Disk-free headroom on the root filesystem. <10% free or
	// <1GiB free surfaces as degraded so operators see it before
	// the log volume actually saturates. Uses syscall.Statfs; if
	// that fails (e.g. non-POSIX tmpfs in a test), we silently skip.
	if free, total, ok := diskSpace(s.root); ok && total > 0 {
		pctFree := float64(free) / float64(total) * 100.0
		const minGiB = 1 << 30
		switch {
		case free < minGiB:
			problems = append(problems, fmt.Sprintf(
				"root: disk free %s (<1GiB) on %s", formatBytes(free), s.root))
		case pctFree < 10.0:
			problems = append(problems, fmt.Sprintf(
				"root: %.1f%% free on %s (<10%%)", pctFree, s.root))
		}
	}

	resp := `{"status":"ok"}`
	if len(problems) > 0 {
		// Hand-build the JSON: problems is user-facing text with
		// arbitrary characters. json.Marshal keeps quoting correct.
		buf, _ := json.Marshal(map[string]any{
			"status":   "degraded",
			"problems": problems,
		})
		resp = string(buf)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, resp)
}

// diskSpace returns (free, total, ok) bytes for the filesystem containing
// path. ok=false when the host syscall fails -- caller should treat that as
// "couldn't check, move on" rather than degraded. Implementations live in
// diskspace_unix.go and diskspace_windows.go because the underlying syscalls
// don't share an API.

// formatBytes prints a compact GiB/MiB/KiB string for one number.
// Precision is coarse on purpose -- health output is for skimming,
// not for exact accounting.
func formatBytes(n uint64) string {
	const (
		ki = 1 << 10
		mi = 1 << 20
		gi = 1 << 30
	)
	switch {
	case n >= gi:
		return fmt.Sprintf("%.1fGiB", float64(n)/gi)
	case n >= mi:
		return fmt.Sprintf("%.0fMiB", float64(n)/mi)
	case n >= ki:
		return fmt.Sprintf("%.0fKiB", float64(n)/ki)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func (s *Server) handleAppend(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	if runID == "" || nodeID == "" {
		http.Error(w, "runID and nodeID required", http.StatusBadRequest)
		return
	}
	if err := validateID(runID); err != nil {
		http.Error(w, "runID: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateID(nodeID); err != nil {
		http.Error(w, "nodeID: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Cap body size to avoid a pathological client filling disk with
	// a single request. Bulk of append traffic is tiny lines; 4 MiB
	// per POST is generous.
	body := http.MaxBytesReader(w, r.Body, 4<<20)
	defer r.Body.Close()

	path, err := s.pathFor(runID, nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Serialize per-file opens so concurrent POSTs don't interleave
	// partial writes. Most of the cost is fsync; mu is fine for v1.
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	path, err := s.pathFor(runID, nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filter, err := parseLogFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Empty, not 404 -- a node with zero log lines still
			// "exists" as far as the run is concerned.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if filter.passThrough() {
		_, _ = io.Copy(w, f)
		return
	}
	data, rerr := io.ReadAll(f)
	if rerr != nil {
		return
	}
	_, _ = w.Write(filter.apply(data))
}

// logFilter collects the server-side filter knobs for handleRead.
// The same semantics are available client-side (pkg/logs helpers)
// and cluster-side (this handler), so `sparkwing jobs logs --tail N`
// behaves identically against local files and remote log bytes.
type logFilter struct {
	tail  int
	head  int
	lines string // "A:B" inclusive 1-indexed
	grep  string
}

func parseLogFilter(r *http.Request) (logFilter, error) {
	q := r.URL.Query()
	f := logFilter{lines: q.Get("lines"), grep: q.Get("grep")}
	if v := q.Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, fmt.Errorf("invalid tail: %q", v)
		}
		f.tail = n
	}
	if v := q.Get("head"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, fmt.Errorf("invalid head: %q", v)
		}
		f.head = n
	}
	if f.lines != "" {
		if _, _, err := parseLinesRange(f.lines); err != nil {
			return f, err
		}
	}
	return f, nil
}

// passThrough is true when no filter was requested. Lets the GET
// handler stream without buffering the whole file.
func (f logFilter) passThrough() bool {
	return f.tail == 0 && f.head == 0 && f.lines == "" && f.grep == ""
}

// apply filters raw bytes according to the requested knobs. Order:
// grep first (so tail/head count filtered lines), then lines window,
// then tail/head. tail wins if both tail and head are set, matching
// the old semantics of "take the last N".
func (f logFilter) apply(data []byte) []byte {
	text := string(data)
	trailingNL := strings.HasSuffix(text, "\n")
	if trailingNL {
		text = strings.TrimSuffix(text, "\n")
	}
	var lines []string
	if text != "" {
		lines = strings.Split(text, "\n")
	}

	if f.grep != "" {
		kept := lines[:0:0]
		for _, l := range lines {
			if strings.Contains(l, f.grep) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	if f.lines != "" {
		a, b, _ := parseLinesRange(f.lines)
		lines = sliceRange(lines, a, b)
	}
	if f.tail > 0 && len(lines) > f.tail {
		lines = lines[len(lines)-f.tail:]
	} else if f.head > 0 && len(lines) > f.head {
		lines = lines[:f.head]
	}
	if len(lines) == 0 {
		return nil
	}
	out := strings.Join(lines, "\n")
	out += "\n"
	_ = trailingNL
	return []byte(out)
}

// parseLinesRange parses "A:B" into 1-indexed inclusive bounds.
// A must be >= 1; B may be 0 meaning "to end".
func parseLinesRange(spec string) (int, int, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid lines spec %q (want A:B)", spec)
	}
	a, err := strconv.Atoi(parts[0])
	if err != nil || a < 1 {
		return 0, 0, fmt.Errorf("invalid lines start %q", parts[0])
	}
	var b int
	if parts[1] != "" {
		b, err = strconv.Atoi(parts[1])
		if err != nil || b < a {
			return 0, 0, fmt.Errorf("invalid lines end %q", parts[1])
		}
	}
	return a, b, nil
}

// sliceRange returns lines[a-1:b] with 1-indexed inclusive bounds,
// clamped to the actual slice length. b==0 means "until end".
func sliceRange(lines []string, a, b int) []string {
	if a < 1 {
		a = 1
	}
	if a > len(lines) {
		return nil
	}
	if b == 0 || b > len(lines) {
		b = len(lines)
	}
	return lines[a-1 : b]
}

// handleDeleteRun removes every log file for the run (the whole
// runs/<runID> directory). 204 whether or not the dir existed so
// `sparkwing jobs prune` can run repeatedly without babysitting.
func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if err := validateID(runID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runDir := filepath.Join(s.root, "runs", runID)
	if err := os.RemoveAll(runDir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReadRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if err := validateID(runID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runDir := filepath.Join(s.root, "runs", runID)
	entries, err := os.ReadDir(runDir)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	for i, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".log") {
			continue
		}
		nodeID := strings.TrimSuffix(name, ".log")
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "=== %s ===\n", nodeID)
		f, err := os.Open(filepath.Join(runDir, name))
		if err != nil {
			fmt.Fprintf(w, "(error reading %s: %v)\n", nodeID, err)
			continue
		}
		_, _ = io.Copy(w, f)
		_ = f.Close()
	}
}

// handleStream serves a Server-Sent Events (text/event-stream) feed
// of new bytes appended to a node's log. One event per poll cycle
// when there's new data; silent otherwise. Polling over fsnotify
// for v1 simplicity -- 200ms latency is imperceptible for UX and
// avoids per-connection watcher bookkeeping.
//
// Each event's "data:" field contains one line of log content. Empty
// log lines are preserved so the dashboard can reconstruct the
// original file byte-for-byte.
//
// Never self-terminates: the logs service has no way to know when a
// run is done. The caller (dashboard or CLI) closes the connection
// when the run reaches a terminal state.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	path, err := s.pathFor(runID, nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Opening comment keeps the connection "alive" for browsers that
	// buffer until first output.
	fmt.Fprintln(w, ": open")
	flusher.Flush()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	// Heartbeat keeps intermediaries (reverse proxies, SSE polyfills)
	// from timing out idle streams. 15s is safely inside every common
	// LB default.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	var offset int64
	pending := ""
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprintln(w, ": keepalive"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			// Open + stat + read may fail because the file is
			// created lazily on first append. That's expected before
			// the node has logged anything; keep polling.
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			fi, err := f.Stat()
			if err != nil {
				f.Close()
				continue
			}
			if fi.Size() <= offset {
				f.Close()
				continue
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				f.Close()
				continue
			}
			buf := make([]byte, fi.Size()-offset)
			n, _ := io.ReadFull(f, buf)
			f.Close()
			offset += int64(n)

			// Split into complete lines; hold any trailing partial
			// until the next poll so we never emit half a line.
			chunk := pending + string(buf[:n])
			parts := splitKeepPartial(chunk)
			pending = parts.trailing
			for _, line := range parts.complete {
				if _, err := fmt.Fprintf(w, "data: %s\n\n", sseEscape(line)); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}

// splitKeepPartial separates complete lines (ending in "\n") from a
// trailing partial line. Callers hold the partial across polls so
// SSE events always contain a whole line.
type splitResult struct {
	complete []string
	trailing string
}

func splitKeepPartial(s string) splitResult {
	out := splitResult{}
	lines := strings.Split(s, "\n")
	// After Split, the last element is the text following the last
	// "\n" (or the whole string if no "\n" was present). That tail
	// is the partial.
	for i := 0; i < len(lines)-1; i++ {
		out.complete = append(out.complete, lines[i])
	}
	out.trailing = lines[len(lines)-1]
	return out
}

// sseEscape removes characters that would break the "data: ..." line
// contract. Newlines in payloads would split the event; CRs would
// mess with some SSE polyfills. We never generate either in the
// logger, but belt-and-braces since the bytes come from a file.
func sseEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// --- helpers ---

// pathFor computes and validates the filesystem path for a node's
// log file. Returns an error on any component that could escape the
// root (path traversal), rather than attempting to sanitize.
func (s *Server) pathFor(runID, nodeID string) (string, error) {
	if err := validateID(runID); err != nil {
		return "", fmt.Errorf("runID: %w", err)
	}
	if err := validateID(nodeID); err != nil {
		return "", fmt.Errorf("nodeID: %w", err)
	}
	dir := filepath.Join(s.root, "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, nodeID+".log"), nil
}

// validateID rejects path-traversal chars. Node/run IDs in sparkwing
// are ASCII alphanumeric + hyphens + underscores; this is a
// conservative guard rather than a full charset definition.
func validateID(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	if strings.Contains(s, "..") || strings.ContainsAny(s, "/\\") {
		return errors.New("invalid characters")
	}
	return nil
}

func withRequestLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rw, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer's Flusher so SSE handlers
// that assert *http.Flusher through this wrapper still work. Without
// it, the type assertion fails and streaming endpoints 500 out.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
