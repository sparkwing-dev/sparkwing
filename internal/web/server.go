// Package web serves the sparkwing dashboard: the dashboard-owned
// slice of /api/v1/* (logs, events SSE), a reverse proxy of the rest
// to the controller, and the embedded Next.js bundle at /. The bundle
// lives under pkg/orchestrator/web/next-out/ and is populated by
// `wing install` (or the Dockerfile) before `go build` runs.
package web

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

//go:embed all:next-out
var nextBundle embed.FS

// HandlerOptions bundles everything the dashboard handler needs.
// Zero value is the local-mode default.
type HandlerOptions struct {
	Backend       backend.Backend
	Paths         orchestrator.Paths
	ControllerURL string // if set, /api/v1/* proxies to this URL
	LogsURL       string // sparkwing-logs base URL (for /api/v1/health/services probe)
	Token         string // controller bearer token (cluster mode)
	// APIURL is injected into the SPA HTML as window.__SPARKWING_API_URL__.
	// Empty means same-origin.
	APIURL        string
	ExtraServices []HealthService
	// RequireLogin gates the browser-facing surface behind the
	// session-cookie flow. Disabled in laptop-local dev where an empty
	// tokens table would make the login redirect a dead-end loop.
	RequireLogin bool
}

// Serve starts the dashboard in local mode, reading state from the
// SQLite store at paths.StateDB().
func Serve(ctx context.Context, paths orchestrator.Paths, addr string) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	return ServeWithOptions(ctx,
		HandlerOptions{Backend: backend.NewStoreBackend(st, paths, nil), Paths: paths},
		addr)
}

// ServeWithOptions is the cluster-mode entry point.
func ServeWithOptions(ctx context.Context, opts HandlerOptions, addr string) error {
	if err := opts.Paths.EnsureRoot(); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:         addr,
		Handler:      HandlerFromOptions(opts),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(os.Stderr, "sparkwing web: serving http://%s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// HandlerFromOptions returns the full dashboard HTTP handler.
func HandlerFromOptions(opts HandlerOptions) http.Handler {
	// Inner mux is authenticated; outer router exposes login/logout
	// + /api/health unauthenticated to avoid a login catch-22.
	authedMux := http.NewServeMux()

	// Method+path-param routes register before the catch-all proxy so
	// Go 1.22's ServeMux picks these over /api/v1/.
	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs", runLogsHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs/search", runLogsSearchHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/grep", runsGrepHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}", nodeLogsHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/stream", nodeLogStreamHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/events/stream", eventsStreamHandler(opts.Backend))

	// Aggregate health probe lives on the dashboard because only the
	// dashboard knows every sibling service URL in a deployment.
	services := append(defaultServices(opts, opts.LogsURL), opts.ExtraServices...)
	authedMux.HandleFunc("/api/v1/health/services", healthServicesHandler(services, opts.Token))

	authedMux.HandleFunc("GET /api/v1/capabilities", CapabilitiesHandler(opts.Backend))
	authedMux.HandleFunc("/api/v1/pipelines", pipelinesHandler())

	if opts.LogsURL != "" {
		authedMux.Handle("/api/v1/logs/", controllerProxy(opts.LogsURL, opts.Token))
	}
	if opts.ControllerURL != "" {
		authedMux.Handle("/api/v1/", controllerProxy(opts.ControllerURL, opts.Token))
	} else {
		authedMux.HandleFunc("/api/v1/", notImplementedHandler)
	}

	subFS, err := fs.Sub(nextBundle, "next-out")
	if err != nil {
		panic(fmt.Sprintf("web: embed fs.Sub failed: %v", err))
	}
	authedMux.Handle("/", spaHandler(subFS, opts))

	router := http.NewServeMux()
	router.HandleFunc("/api/health", healthHandler)
	router.HandleFunc("GET /login", loginPageHandler(opts))
	// Shared bucket across /login + /login/bootstrap so an attacker
	// probing both endpoints can't spend its budget twice.
	loginLimiter := newRateLimiter(loginRateBurst, loginRateWindow)
	router.Handle("POST /login",
		rateLimitMiddleware(loginLimiter, http.HandlerFunc(loginSubmitHandler(opts))))
	router.Handle("POST /login/bootstrap",
		rateLimitMiddleware(loginLimiter, http.HandlerFunc(bootstrapSubmitHandler(opts))))
	router.HandleFunc("POST /logout", logoutHandler(opts))
	router.Handle("/", sessionAuthMiddleware(opts, authedMux))
	return router
}

// spaHandler serves the Next.js static export, templating HTML files
// to inject window globals and falling through to index.html for SPA
// client-side routes. Next 16 emits top-level <route>.html; older
// exports (Next <= 15) used <route>/index.html, so both layouts work.
func spaHandler(bundleFS fs.FS, opts HandlerOptions) http.Handler {
	fileServer := http.FileServer(http.FS(bundleFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			serveTemplatedHTML(w, r, bundleFS, "index.html", opts)
			return
		}

		if strings.HasSuffix(p, ".html") && isTemplatedPath(p) {
			serveTemplatedHTML(w, r, bundleFS, p, opts)
			return
		}

		// Stat <route>.html before the directory check: Next 16's export
		// creates a same-named directory of Turbopack internals that
		// http.FileServer would 301-redirect into a dead end.
		if _, err := fs.Stat(bundleFS, p+".html"); err == nil {
			serveTemplatedHTML(w, r, bundleFS, p+".html", opts)
			return
		}

		if _, err := fs.Stat(bundleFS, p+"/index.html"); err == nil {
			serveTemplatedHTML(w, r, bundleFS, p+"/index.html", opts)
			return
		}

		if info, err := fs.Stat(bundleFS, p); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}

		serveTemplatedHTML(w, r, bundleFS, "index.html", opts)
	})
}

// isTemplatedPath returns true for HTML files that contain the
// runtime-config markers (every top-level Next page via layout.tsx).
func isTemplatedPath(p string) bool {
	return !strings.HasPrefix(p, "_next/") && !strings.HasPrefix(p, "next-dev/")
}

func serveTemplatedHTML(w http.ResponseWriter, _ *http.Request, bundleFS fs.FS, name string, opts HandlerOptions) {
	raw, err := fs.ReadFile(bundleFS, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	// Escape values so quotes/backslashes in a token don't break the
	// <script> literal. Markers are inside JSON strings in layout.tsx;
	// only the inside is replaced.
	body := bytes.ReplaceAll(raw,
		[]byte("__SPARKWING_TOKEN_MARKER__"),
		[]byte(jsStringEscape(opts.Token)))
	body = bytes.ReplaceAll(body,
		[]byte("__SPARKWING_API_URL_MARKER__"),
		[]byte(jsStringEscape(opts.APIURL)))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// jsStringEscape escapes characters that would break out of the
// double-quoted JS string literal in layout.tsx.
func jsStringEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '<': // avoid breaking out of <script>
			b.WriteString(`<`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func controllerProxy(controllerURL, token string) http.Handler {
	u, err := url.Parse(controllerURL)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, fmt.Sprintf("bad controller URL: %v", err), http.StatusInternalServerError)
		})
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			if token != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+token)
			}
		},
	}
	return proxy
}

func notImplementedHandler(w http.ResponseWriter, _ *http.Request) {
	http.Error(w,
		"this endpoint requires --controller mode; start the dashboard with --controller URL",
		http.StatusNotImplemented)
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// serveLogStream proxies the logs-service SSE stream through the
// dashboard. Closing either end tears the whole thing down via context
// cancellation.
func serveLogStream(b backend.Backend, w http.ResponseWriter, r *http.Request, runID, nodeID string) {
	body, err := b.StreamNodeLog(r.Context(), runID, nodeID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if body == nil {
		// Source doesn't support streaming (disk in local mode); 501
		// lets the dashboard fall back to polling.
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	defer body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	format := negotiateLogFormat(r)
	if format == formatRaw {
		buf := make([]byte, 4096)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				flusher.Flush()
			}
			if err != nil {
				return
			}
		}
	}

	streamPrettySSE(body, w, flusher, format)
}

// serveEventsStream tails the run's events table as an SSE stream:
// backlog after Last-Event-ID, then poll every 250ms for new rows
// until the run is terminal. Each frame uses ev.Seq as the SSE id so
// the browser's automatic Last-Event-ID retry resumes cleanly.
func serveEventsStream(b backend.Backend, w http.ResponseWriter, r *http.Request, runID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}

	// Verify up-front so a typo returns 404 instead of an open stream
	// that never produces anything.
	run, err := b.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	afterSeq := parseLastEventID(r.Header.Get("Last-Event-ID"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable nginx proxy buffering so events land within one poll tick.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Open-comment keeps the connection alive while we wait on the
	// first poll; EventSource tolerates leading comment lines.
	_, _ = w.Write([]byte(": open\n\n"))
	flusher.Flush()

	ctx := r.Context()
	const (
		pollInterval = 250 * time.Millisecond
		pageSize     = 500
		// Re-read the run row every N event ticks so a long stream
		// doesn't hammer the store for a single column we only need
		// for termination detection.
		runStatusEveryN = 8
		heartbeatEvery  = 20 * time.Second
	)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	tick := 0
	lastHB := time.Now()
	terminal := isRunTerminal(run.Status)

	for {
		events, err := b.ListEventsAfter(ctx, runID, afterSeq, pageSize)
		if err != nil {
			// Closing is the cleanest signal in an already-open SSE
			// stream; the client's onerror triggers fallback polling.
			return
		}
		for _, ev := range events {
			if !writeEventSSE(w, ev) {
				return
			}
			afterSeq = ev.Seq
		}
		if len(events) > 0 {
			flusher.Flush()
			lastHB = time.Now()
		}

		// On terminal-and-drained, send an end-of-stream hint so the
		// client closes cleanly without waiting for onerror.
		if terminal && len(events) == 0 {
			_, _ = w.Write([]byte("event: stream_end\ndata: {}\n\n"))
			flusher.Flush()
			return
		}

		// Some proxies (dev-mode Next.js included) reap idle SSE
		// streams after ~30s without a keepalive.
		if time.Since(lastHB) >= heartbeatEvery {
			if _, werr := w.Write([]byte(": keepalive\n\n")); werr != nil {
				return
			}
			flusher.Flush()
			lastHB = time.Now()
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		tick++
		if tick%runStatusEveryN == 0 && !terminal {
			if fresh, rerr := b.GetRun(ctx, runID); rerr == nil && fresh != nil {
				terminal = isRunTerminal(fresh.Status)
			}
		}
	}
}

// parseLastEventID parses the browser-sent Last-Event-ID. Missing or
// invalid values resume from 0 (full backlog).
func parseLastEventID(h string) int64 {
	if h == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(h), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// isRunTerminal reports whether a run status means no more events
// will be emitted. Unknown statuses are treated as still-running so
// the stream stays open.
func isRunTerminal(status string) bool {
	switch status {
	case "success", "failed", "cancelled":
		return true
	}
	return false
}

// writeEventSSE writes one event row as an SSE frame. Returns false on
// write failure so the caller can exit the loop.
func writeEventSSE(w io.Writer, ev store.Event) bool {
	type wire struct {
		RunID   string          `json:"run_id"`
		Seq     int64           `json:"seq"`
		NodeID  string          `json:"node_id,omitempty"`
		Kind    string          `json:"kind"`
		TS      time.Time       `json:"ts"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	body, err := json.Marshal(wire{
		RunID:   ev.RunID,
		Seq:     ev.Seq,
		NodeID:  ev.NodeID,
		Kind:    ev.Kind,
		TS:      ev.TS,
		Payload: ev.Payload,
	})
	if err != nil {
		return false
	}
	frame := fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, body)
	_, werr := w.Write([]byte(frame))
	return werr == nil
}

func serveLogs(b backend.Backend, w http.ResponseWriter, r *http.Request, runID, nodeID string) {
	format := negotiateLogFormat(r)
	w.Header().Set("Content-Type", contentTypeFor(format))
	if nodeID != "" {
		content, err := b.ReadNodeLog(r.Context(), runID, nodeID, backend.ReadOpts{})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if len(content) == 0 {
			return
		}
		if format == formatRaw {
			_, _ = w.Write(content)
			return
		}
		renderJSONL(content, w, format)
		return
	}

	nodes, err := b.ListNodes(r.Context(), runID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for i, n := range nodes {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "=== %s (%s) ===\n", n.NodeID, n.Outcome)
		content, err := b.ReadNodeLog(r.Context(), runID, n.NodeID, backend.ReadOpts{})
		if err != nil {
			fmt.Fprintf(w, "(error: %v)\n", err)
			continue
		}
		if format == formatRaw {
			_, _ = w.Write(content)
			continue
		}
		renderJSONL(content, w, format)
	}
}

func runLogsHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveLogs(b, w, r, r.PathValue("id"), "")
	}
}

// runLogsSearchHandler greps every node's log file in one run for
// case-insensitive substring matches of `q`. Returns matches with
// (node_id, line, content) so the dashboard's "search all logs"
// box can render results without pulling N node-log payloads to
// the browser. limit caps total matches returned (default 500,
// max 5000); the server walks every line so the total count
// reflects the full run.
// displayBodyForLogLine mirrors the frontend's parseJSONLLogs +
// recordToLine: only records that produce a visible line in the
// dashboard contribute to the display-line counter, and the body
// matched is what the user actually sees (msg / attrs / synthetic
// "[skipped: …]"), not the raw NDJSON framing.
//
// Returns (body, true) when the line corresponds to a displayed row,
// or ("", false) for framing events the parser swallows (node_start,
// step_start, step_end, node_end, run_summary).
func displayBodyForLogLine(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed[0] != '{' {
		return raw, true
	}
	var rec struct {
		Event string                 `json:"event"`
		Msg   string                 `json:"msg"`
		Level string                 `json:"level"`
		Attrs map[string]interface{} `json:"attrs,omitempty"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return raw, true
	}
	switch rec.Event {
	case "node_start", "step_start", "step_end", "node_end", "run_summary":
		return "", false
	case "step_skipped":
		reason, _ := rec.Attrs["reason"].(string)
		if reason != "" {
			return "[skipped: " + reason + "]", true
		}
		return "[skipped]", true
	}
	if rec.Msg != "" {
		return rec.Msg, true
	}
	if len(rec.Attrs) > 0 {
		attrBytes, _ := json.Marshal(rec.Attrs)
		return string(attrBytes), true
	}
	return "", true
}

func runLogsSearchHandler(b backend.Backend) http.HandlerFunc {
	type match struct {
		NodeID  string `json:"node_id"`
		Line    int    `json:"line"`
		Content string `json:"content"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeErr(w, http.StatusBadRequest, errors.New("q is required"))
			return
		}
		limit := 500
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > 5000 {
			limit = 5000
		}
		needle := strings.ToLower(q)
		nodes, err := b.ListNodes(r.Context(), runID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// Read + grep each node's log concurrently. ReadNodeLog is a
		// per-node HTTP hop in cluster-mode (ClientBackend), so the
		// difference between serial and parallel here is N round-trips
		// vs. one round-trip wall-clock per search.
		type nodeResult struct {
			matches []match
			count   int
			order   int
		}
		const fanout = 8
		sem := make(chan struct{}, fanout)
		results := make([]nodeResult, len(nodes))
		var wg sync.WaitGroup
		for i, n := range nodes {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, nodeID string) {
				defer wg.Done()
				defer func() { <-sem }()
				content, err := b.ReadNodeLog(r.Context(), runID, nodeID, backend.ReadOpts{})
				if err != nil || len(content) == 0 {
					return
				}
				sc := bufio.NewScanner(bytes.NewReader(content))
				sc.Buffer(make([]byte, 1<<16), 1<<20)
				displayLine := 0
				local := nodeResult{order: i}
				for sc.Scan() {
					raw := sc.Text()
					body, ok := displayBodyForLogLine(raw)
					if !ok {
						continue
					}
					displayLine++
					if !strings.Contains(strings.ToLower(body), needle) {
						continue
					}
					local.count++
					if local.count <= limit {
						local.matches = append(local.matches, match{
							NodeID:  nodeID,
							Line:    displayLine,
							Content: body,
						})
					}
				}
				results[i] = local
			}(i, n.NodeID)
		}
		wg.Wait()
		matches := make([]match, 0, 64)
		total := 0
		for _, res := range results {
			total += res.count
			for _, m := range res.matches {
				if len(matches) >= limit {
					break
				}
				matches = append(matches, m)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"query":   q,
			"results": matches,
			"total":   total,
		})
	}
}

// runsGrepHandler walks recent runs matching the supplied filter set
// and substring-greps every node log. Mirrors `sparkwing runs grep`
// from the CLI: same filter shape, same row schema. Each (run, node)
// log read fans out so the wall-clock cost is dominated by the
// slowest single read instead of summing all of them.
//
// Matching uses displayBodyForLogLine, so node id / step framing in
// NDJSON metadata doesn't generate spurious hits — only what the
// dashboard's Logs tab would actually display.
func runsGrepHandler(b backend.Backend) http.HandlerFunc {
	type match struct {
		RunID    string `json:"run_id"`
		Pipeline string `json:"pipeline"`
		NodeID   string `json:"node_id"`
		Line     int    `json:"line"`
		Content  string `json:"content"`
	}
	type runMeta struct {
		Status     string `json:"status"`
		Pipeline   string `json:"pipeline"`
		StartedAt  string `json:"started_at,omitempty"`
		FinishedAt string `json:"finished_at,omitempty"`
		GitBranch  string `json:"git_branch,omitempty"`
		GitSHA     string `json:"git_sha,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeErr(w, http.StatusBadRequest, errors.New("q is required"))
			return
		}
		needle := strings.ToLower(q)
		runLimit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				runLimit = n
			}
		}
		if runLimit > 1000 {
			runLimit = 1000
		}
		maxMatches := 5
		if v := r.URL.Query().Get("max_matches"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				maxMatches = n
			}
		}
		filter := store.RunFilter{
			Pipelines: r.URL.Query()["pipeline"],
			Statuses:  r.URL.Query()["status"],
			Limit:     runLimit,
		}
		if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
			if d, err := time.ParseDuration(sinceStr); err == nil && d > 0 {
				filter.Since = time.Now().Add(-d)
			}
		}
		runs, err := b.ListRuns(r.Context(), filter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		runs = applyGrepExcludes(runs, grepExcludes{
			pipelines:   r.URL.Query()["npipeline"],
			statuses:    r.URL.Query()["nstatus"],
			branches:    r.URL.Query()["nbranch"],
			shaPrefixes: r.URL.Query()["nsha"],
		})
		branches := r.URL.Query()["branch"]
		shaPrefixes := r.URL.Query()["sha"]
		if len(branches) > 0 || len(shaPrefixes) > 0 {
			runs = filterRunsByBranchSHA(runs, branches, shaPrefixes)
		}
		// Build (run, node) work units up front so the worker pool can
		// drain them with a single fanout cap rather than nested
		// per-run goroutines.
		type work struct {
			run    *store.Run
			nodeID string
		}
		var units []work
		for _, run := range runs {
			nodes, err := b.ListNodes(r.Context(), run.ID)
			if err != nil {
				continue
			}
			for _, n := range nodes {
				units = append(units, work{run: run, nodeID: n.NodeID})
			}
		}
		const fanout = 8
		sem := make(chan struct{}, fanout)
		type unitResult struct {
			matches []match
			count   int
		}
		results := make([]unitResult, len(units))
		var wg sync.WaitGroup
		for i, u := range units {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, u work) {
				defer wg.Done()
				defer func() { <-sem }()
				content, err := b.ReadNodeLog(r.Context(), u.run.ID, u.nodeID, backend.ReadOpts{})
				if err != nil || len(content) == 0 {
					return
				}
				sc := bufio.NewScanner(bytes.NewReader(content))
				sc.Buffer(make([]byte, 1<<16), 1<<20)
				displayLine := 0
				var local unitResult
				for sc.Scan() {
					body, ok := displayBodyForLogLine(sc.Text())
					if !ok {
						continue
					}
					displayLine++
					if !strings.Contains(strings.ToLower(body), needle) {
						continue
					}
					local.count++
					if maxMatches == 0 || local.count <= maxMatches {
						local.matches = append(local.matches, match{
							RunID:    u.run.ID,
							Pipeline: u.run.Pipeline,
							NodeID:   u.nodeID,
							Line:     displayLine,
							Content:  body,
						})
					}
				}
				results[i] = local
			}(i, u)
		}
		wg.Wait()
		var matches []match
		total := 0
		hitRuns := map[string]bool{}
		for _, res := range results {
			total += res.count
			for _, m := range res.matches {
				hitRuns[m.RunID] = true
			}
			matches = append(matches, res.matches...)
		}
		runIndex := make(map[string]*store.Run, len(runs))
		for _, run := range runs {
			runIndex[run.ID] = run
		}
		runsMeta := make(map[string]runMeta, len(hitRuns))
		for id := range hitRuns {
			run := runIndex[id]
			if run == nil {
				continue
			}
			runsMeta[id] = runMeta{
				Status:     run.Status,
				Pipeline:   run.Pipeline,
				StartedAt:  run.StartedAt.Format(time.RFC3339Nano),
				FinishedAt: timeOrEmptyPtr(run.FinishedAt),
				GitBranch:  run.GitBranch,
				GitSHA:     run.GitSHA,
				Error:      run.Error,
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"query":        q,
			"matches":      matches,
			"runs":         runsMeta,
			"total":        total,
			"runs_scanned": len(runs),
		})
	}
}

func timeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func timeOrEmptyPtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

type grepExcludes struct {
	pipelines   []string
	statuses    []string
	branches    []string
	shaPrefixes []string
}

func applyGrepExcludes(runs []*store.Run, ex grepExcludes) []*store.Run {
	if len(ex.pipelines)+len(ex.statuses)+len(ex.branches)+len(ex.shaPrefixes) == 0 {
		return runs
	}
	out := runs[:0]
	for _, run := range runs {
		if containsExact(ex.pipelines, run.Pipeline) {
			continue
		}
		if containsExact(ex.statuses, run.Status) {
			continue
		}
		if containsExact(ex.branches, run.GitBranch) {
			continue
		}
		excludedBySHA := false
		for _, p := range ex.shaPrefixes {
			if p != "" && strings.HasPrefix(run.GitSHA, p) {
				excludedBySHA = true
				break
			}
		}
		if excludedBySHA {
			continue
		}
		out = append(out, run)
	}
	return out
}

// filterRunsByBranchSHA mirrors the CLI's --branch / --sha narrowing.
// Empty filter lists short-circuit at the call site so we can keep
// the body trivial.
func filterRunsByBranchSHA(runs []*store.Run, branches, shaPrefixes []string) []*store.Run {
	out := runs[:0]
	for _, run := range runs {
		if len(branches) > 0 {
			if !containsExact(branches, run.GitBranch) {
				continue
			}
		}
		if len(shaPrefixes) > 0 {
			matched := false
			for _, p := range shaPrefixes {
				if p != "" && strings.HasPrefix(run.GitSHA, p) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, run)
	}
	return out
}

func containsExact(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func nodeLogsHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveLogs(b, w, r, r.PathValue("id"), r.PathValue("node"))
	}
}

func nodeLogStreamHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveLogStream(b, w, r, r.PathValue("id"), r.PathValue("node"))
	}
}

func eventsStreamHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveEventsStream(b, w, r, r.PathValue("id"))
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
