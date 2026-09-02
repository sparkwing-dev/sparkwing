package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

// safety: NAME_MAX is 255 bytes, so the cap covers the mapped node id plus its ".log" suffix.
const maxFileNameBytes = 255

// Server handles HTTP requests against a filesystem-backed log store.
type Server struct {
	root     string
	logger   *slog.Logger
	dirMode  os.FileMode
	fileMode os.FileMode
	mu       sync.Mutex

	controllerURL string
	authCache     sync.Map
	authCacheTTL  time.Duration
	authHTTP      *http.Client
}

// New constructs a Server rooted at dir (created if absent). A nil
// logger uses slog.Default.
func New(root string, logger *slog.Logger) (*Server, error) {
	return newServer(root, logger, false)
}

// NewPrivate constructs a Server whose directories and log files are
// owner-only. It is for Sparkwing's default local home; operator-selected
// shared and PVC roots should use [New].
func NewPrivate(root string, logger *slog.Logger) (*Server, error) {
	return newServer(root, logger, true)
}

func newServer(root string, logger *slog.Logger, private bool) (*Server, error) {
	if root == "" {
		return nil, errors.New("logs: root is required")
	}
	dirMode, fileMode := os.FileMode(0o755), os.FileMode(0o644)
	if private {
		dirMode, fileMode = fssecure.DirMode, fssecure.FileMode
	}
	if err := ensureServerDir(root, dirMode); err != nil {
		return nil, fmt.Errorf("logs: mkdir %s: %w", root, err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{root: root, logger: logger, dirMode: dirMode, fileMode: fileMode}, nil
}

func ensureServerDir(path string, mode os.FileMode) error {
	if mode == fssecure.DirMode {
		return fssecure.EnsureDir(path)
	}
	return os.MkdirAll(path, mode)
}

func (s *Server) ensureDir(path string) error {
	return ensureServerDir(path, s.dirMode)
}

func (s *Server) writeFile(path string, body []byte) error {
	if s.fileMode == fssecure.FileMode {
		return fssecure.WriteFile(path, body)
	}
	return os.WriteFile(path, body, s.fileMode)
}

func (s *Server) openAppend(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, s.fileMode)
	if err != nil {
		return nil, err
	}
	if s.fileMode != fssecure.FileMode {
		return f, nil
	}
	if err := fssecure.TightenOpen(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
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

	mux.Handle("POST /api/v1/logs/{runID}/{nodeID}", s.requireScope(scopeLogsWrite, http.HandlerFunc(s.handleAppend)))
	mux.Handle("GET /api/v1/logs/{runID}/{nodeID}", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleRead)))
	mux.Handle("GET /api/v1/logs/{runID}", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleReadRun)))
	mux.Handle("DELETE /api/v1/logs/{runID}", s.requireScope(scopeLogsWrite, http.HandlerFunc(s.handleDeleteRun)))
	mux.Handle("GET /api/v1/logs/{runID}/{nodeID}/stream", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleStream)))

	mux.Handle("GET /api/v1/logs/search", s.requireScope(scopeLogsRead, http.HandlerFunc(s.handleSearch)))

	authed := s.authMiddleware(mux)

	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	router.Handle("GET /metrics", promhttp.Handler())
	router.Handle("/", authed)
	return otelutil.WrapHandler("sparkwing-logs", withRequestLog(router, s.logger))
}

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

func (s *Server) authDisabled() bool {
	return s.controllerURL == ""
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	if s.authDisabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := extractBearer(r)
		if err != nil {
			writeAuthErrorJSON(w, http.StatusUnauthorized, AuthErrorBody{
				Error:   "unauthenticated",
				Message: err.Error(),
			})
			return
		}
		p, err := s.authenticate(r.Context(), raw)
		if err != nil {
			writeAuthErrorJSON(w, http.StatusUnauthorized, AuthErrorBody{
				Error:   "unauthenticated",
				Message: err.Error(),
			})
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithLogsPrincipal(r.Context(), p)))
	})
}

func (s *Server) requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := logsPrincipalFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if p.hasScope(scopeAdmin) || p.hasScope(scope) {
			next.ServeHTTP(w, r)
			return
		}
		writeAuthErrorJSON(w, http.StatusForbidden, AuthErrorBody{
			Error:        "missing_scope",
			MissingScope: scope,
			Principal:    p.label(),
			Message:      "token lacks required scope: " + scope,
		})
	})
}

func (p *logsPrincipal) label() string {
	if p == nil {
		return ""
	}
	if p.Kind == "" {
		return p.Name
	}
	return p.Kind + ":" + p.Name
}

func writeAuthErrorJSON(w http.ResponseWriter, status int, body AuthErrorBody) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix)), nil
}

func (s *Server) authenticate(ctx context.Context, raw string) (*logsPrincipal, error) {
	if raw == "" {
		return nil, errors.New("missing bearer token")
	}

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
	// safety: an unauthenticated controller answers whoami 200 for any string, so its anonymous principal is not a caller.
	if body.Kind == "none" || body.Principal == "unauthed" {
		return nil, errors.New("controller resolved the token to its unauthenticated principal")
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
func ServeWithTokens(ctx context.Context, root, addr, controllerURL string, logger *slog.Logger) error {
	return serveWithTokens(ctx, root, addr, controllerURL, logger, false)
}

// ServePrivateWithTokens serves an owner-only local log root. Operator-chosen
// shared and PVC roots should use [ServeWithTokens].
func ServePrivateWithTokens(ctx context.Context, root, addr, controllerURL string, logger *slog.Logger) error {
	return serveWithTokens(ctx, root, addr, controllerURL, logger, true)
}

func serveWithTokens(ctx context.Context, root, addr, controllerURL string, logger *slog.Logger, private bool) error {
	var s *Server
	var err error
	if private {
		s, err = NewPrivate(root, logger)
	} else {
		s, err = New(root, logger)
	}
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
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info(
			"logs service listening",
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

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	var problems []string

	authState := "enabled"
	if s.authDisabled() {
		authState = "disabled"
	}

	canary := filepath.Join(s.root, ".health-check")
	if err := s.writeFile(canary, []byte("ok")); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status":"degraded","auth":%q,"problems":["root: %s"]}`,
			authState, strings.ReplaceAll(err.Error(), `"`, `\"`))
		return
	}
	_ = os.Remove(canary)

	if free, total, ok := diskSpace(s.root); ok && total > 0 {
		pctFree := float64(free) / float64(total) * 100.0
		const minGiB = 1 << 30
		switch {
		case free < minGiB:
			problems = append(problems, fmt.Sprintf(
				"root: disk free %s (<1GiB) on %s", formatBytes(free), s.root,
			))
		case pctFree < 10.0:
			problems = append(problems, fmt.Sprintf(
				"root: %.1f%% free on %s (<10%%)", pctFree, s.root,
			))
		}
	}

	resp := fmt.Sprintf(`{"status":"ok","auth":%q}`, authState)
	if len(problems) > 0 {
		buf, _ := json.Marshal(map[string]any{
			"status":   "degraded",
			"auth":     authState,
			"problems": problems,
		})
		resp = string(buf)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, resp)
}

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
	if err := validateNodeID(nodeID); err != nil {
		http.Error(w, "nodeID: "+err.Error(), http.StatusBadRequest)
		return
	}

	body := http.MaxBytesReader(w, r.Body, 4<<20)
	defer r.Body.Close()

	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer func() { _ = root.Close() }()
	if err := s.ensureRunDir(root, runID); err != nil {
		s.storeError(w, "create run dir", err)
		return
	}

	// safety: mu serializes concurrent POSTs to the same file so writes don't interleave.
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.openAppend(root, nodePath(runID, nodeID))
	if err != nil {
		s.storeError(w, "open node log", err)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		s.storeError(w, "write node log", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	if err := validateIDs(runID, nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filter, err := parseLogFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(nodePath(runID, nodeID))
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		s.storeError(w, "open node log", err)
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

type logFilter struct {
	tail  int
	head  int
	lines string
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

func (f logFilter) passThrough() bool {
	return f.tail == 0 && f.head == 0 && f.lines == "" && f.grep == ""
}

func (f logFilter) apply(data []byte) []byte {
	text := string(data)
	trailingNL := strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	type line struct {
		text       string
		terminated bool
	}
	var lines []line
	if len(data) > 0 {
		parts := strings.Split(text, "\n")
		lines = make([]line, len(parts))
		for i, part := range parts {
			lines[i] = line{text: part, terminated: i < len(parts)-1 || trailingNL}
		}
	}

	if f.grep != "" {
		kept := lines[:0:0]
		for _, l := range lines {
			if strings.Contains(l.text, f.grep) {
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
	var out strings.Builder
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(line.text)
	}
	if lines[len(lines)-1].terminated {
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

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

func sliceRange[T any](lines []T, a, b int) []T {
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

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if err := validateID(runID); err != nil {
		http.Error(w, "runID: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(runID); err != nil {
		s.storeError(w, "remove run dir", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReadRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if err := validateID(runID); err != nil {
		http.Error(w, "runID: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer func() { _ = root.Close() }()
	entries, err := readRunDir(root, runID)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		s.storeError(w, "read run dir", err)
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
		f, err := root.Open(filepath.Join(runID, name))
		if err != nil {
			fmt.Fprintf(w, "(error reading %s)\n", nodeID)
			continue
		}
		_, _ = io.Copy(w, f)
		_ = f.Close()
	}
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	if err := validateIDs(runID, nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := nodePath(runID, nodeID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer func() { _ = root.Close() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, ": open")
	flusher.Flush()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
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
			f, err := root.Open(path)
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

type splitResult struct {
	complete []string
	trailing string
}

func splitKeepPartial(s string) splitResult {
	out := splitResult{}
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines)-1; i++ {
		out.complete = append(out.complete, lines[i])
	}
	out.trailing = lines[len(lines)-1]
	return out
}

func sseEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// safety: os.Root confines every open, readdir and remove to the runs
// tree, so a symlink planted at runs/<name> cannot be followed out of it.
func (s *Server) openRunsRoot() (*os.Root, error) {
	dir := filepath.Join(s.root, "runs")
	if err := s.ensureDir(dir); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

func (s *Server) ensureRunDir(root *os.Root, runID string) error {
	if err := root.Mkdir(runID, s.dirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if s.dirMode != fssecure.DirMode {
		return nil
	}
	info, err := root.Stat(runID)
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm&fssecure.DirMode != perm {
		return root.Chmod(runID, perm&fssecure.DirMode)
	}
	return nil
}

func readRunDir(root *os.Root, runID string) ([]fs.DirEntry, error) {
	d, err := root.Open(runID)
	if err != nil {
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

func (s *Server) storeError(w http.ResponseWriter, op string, err error) {
	// safety: the detail names an absolute server path, so only the log gets it.
	s.logger.Error("logs store", "op", op, "err", err)
	http.Error(w, "log store error", http.StatusInternalServerError)
}

func validateIDs(runID, nodeID string) error {
	if err := validateID(runID); err != nil {
		return fmt.Errorf("runID: %w", err)
	}
	if err := validateNodeID(nodeID); err != nil {
		return fmt.Errorf("nodeID: %w", err)
	}
	return nil
}

func nodePath(runID, nodeID string) string {
	return filepath.Join(runID, nodeFile(nodeID))
}

// safety: a hierarchical node id collapses to one file name, so the
// join stays exactly one level under the run directory.
func nodeFile(nodeID string) string {
	return strings.ReplaceAll(nodeID, "/", "__") + ".log"
}

func validateID(s string) error {
	if err := validateIDSegment(s); err != nil {
		return err
	}
	if len(s) > maxFileNameBytes {
		return errors.New("too long")
	}
	return nil
}

func validateNodeID(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	for seg := range strings.SplitSeq(s, "/") {
		if err := validateIDSegment(seg); err != nil {
			return err
		}
	}
	if len(nodeFile(s)) > maxFileNameBytes {
		return errors.New("too long")
	}
	return nil
}

func validateIDSegment(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	for _, r := range s {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return errors.New("invalid characters")
		}
	}
	// safety: filepath.Join collapses "." and "..", so only ids that survive Clean unchanged may address a directory.
	if s == "." || s == ".." || filepath.Clean(s) != s {
		return errors.New("invalid characters")
	}
	return nil
}

func withRequestLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rw, r)
		logger.Info(
			"http",
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
