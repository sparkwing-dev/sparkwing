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
	"github.com/sparkwing-dev/sparkwing/internal/docsweb"
	swpaths "github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

//go:embed all:next-out
var nextBundle embed.FS

func VerifyBundleEmbedded() error {
	if bundleSkipReason(nextBundle) != "" {
		return errors.New(missingBundleMessage)
	}
	return nil
}

func BundleSkipReason() string {
	return bundleSkipReason(nextBundle)
}

func bundleSkipReason(bundle fs.FS) string {
	if _, err := fs.Stat(bundle, "next-out/index.html"); err != nil {
		return "dashboard bundle not built in this checkout; run: bash bin/build-web.sh"
	}
	return ""
}

const missingBundleMessage = `dashboard assets are missing from this binary.

The Next.js dashboard bundle is a generated artifact and is not checked
into the sparkwing repository, so "go install" or a source build without
running bin/build-web.sh first produces a binary that compiles cleanly
but serves a silent 404 on every dashboard page.

To run the dashboard locally, install the sparkwing release binary and
use the dashboard subcommand -- not "go install":

  curl -L -o sparkwing \
    https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-linux-amd64
  chmod +x sparkwing && sudo mv sparkwing /usr/local/bin/sparkwing
  sparkwing dashboard start

Release binaries for every platform are listed at:

  https://github.com/sparkwing-dev/sparkwing/releases/latest

To run sparkwing-web in a cluster, use the container image rather than a
source build -- it already has the dashboard bundle baked in.

If you are building from a sparkwing checkout, generate the dashboard
bundle first, then reinstall:

  bash bin/build-web.sh
  go install ./cmd/sparkwing`

type HandlerOptions struct {
	Backend           backend.Backend
	Paths             swpaths.Paths
	ControllerURL     string
	AuthControllerURL string // safety: login stays controller-backed when data reads a shared store directly
	LogsURL           string
	CacheURL          string
	Token             string

	APIURL string

	Version       string
	ExtraServices []HealthService

	RequireLogin bool
}

func Serve(ctx context.Context, paths swpaths.Paths, addr string) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	if rw, err := store.Open(paths.StateDB()); err != nil {
		return err
	} else {
		_ = rw.Close()
	}
	st, err := store.OpenReadOnly(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return ServeWithOptions(ctx,
		HandlerOptions{Backend: backend.NewStoreBackend(st, paths, nil), Paths: paths},
		addr)
}

func ServeWithOptions(ctx context.Context, opts HandlerOptions, addr string) error {
	if err := validateAuthOptions(opts); err != nil {
		return err
	}
	if err := VerifyBundleEmbedded(); err != nil {
		return err
	}
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

func HandlerFromOptions(opts HandlerOptions) http.Handler {
	subFS, err := fs.Sub(nextBundle, "next-out")
	if err != nil {
		panic(fmt.Sprintf("web: embed fs.Sub failed: %v", err)) //nolint:forbidigo // unreachable post-VerifyBundleEmbedded; build-time invariant
	}
	return HandlerFromOptionsWithBundle(opts, subFS)
}

func HandlerFromOptionsWithBundle(opts HandlerOptions, bundleFS fs.FS) http.Handler {
	if err := validateAuthOptions(opts); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "dashboard authentication unavailable: "+err.Error(), http.StatusServiceUnavailable)
		})
	}
	authedMux := http.NewServeMux()

	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs", runLogsHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs/search", runLogsSearchHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/grep", runsGrepHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}", nodeLogsHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/stream", nodeLogStreamHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/runs/{id}/events/stream", eventsStreamHandler(opts.Backend))

	services := append(defaultServices(opts, opts.LogsURL), opts.ExtraServices...)
	authedMux.HandleFunc("/api/v1/health/services", healthServicesHandler(services, opts.Token))

	authedMux.HandleFunc("GET /api/v1/capabilities", CapabilitiesHandler(opts.Backend))
	authedMux.HandleFunc("/api/v1/pipelines", pipelinesHandler())
	authedMux.HandleFunc("GET /api/v1/capacity/profiles", capacityProfilesHandler(opts.Backend))
	authedMux.HandleFunc("GET /api/v1/capacity/profiles/explain", capacityExplainHandler(opts.Backend))

	if opts.LogsURL != "" {
		authedMux.Handle("/api/v1/logs/", controllerProxy(opts.LogsURL, opts.Token, loginRequired(opts)))
	}
	if opts.ControllerURL != "" {
		authedMux.Handle("/api/v1/", controllerProxy(opts.ControllerURL, opts.Token, loginRequired(opts)))
	} else {
		authedMux.HandleFunc("GET /api/v1/runs", ListRunsHandler(opts.Backend))
		authedMux.HandleFunc("GET /api/v1/runs/{id}", GetRunHandler(opts.Backend))
		authedMux.HandleFunc("/api/v1/", notImplementedHandler)
	}

	// safety: /docs belongs on authedMux, above the catch-all: the outer router is
	// unauthenticated, so mounting it there would publish the pages to anyone who can
	// reach the listener while the rest of the dashboard needs a session.
	authedMux.Handle("GET /docs", docsweb.Handler())

	authedMux.Handle("/", spaHandler(bundleFS, opts))

	router := http.NewServeMux()
	router.HandleFunc("/api/health", healthHandler)
	router.HandleFunc("GET /login", loginPageHandler(opts))
	loginLimiter := newRateLimiter(loginRateBurst, loginRateWindow)
	router.Handle("POST /login",
		csrfFormMiddleware(rateLimitMiddleware(loginLimiter, loginSubmitHandler(opts))))
	router.Handle("POST /login/bootstrap",
		csrfFormMiddleware(rateLimitMiddleware(loginLimiter, bootstrapSubmitHandler(opts))))
	router.Handle("POST /logout", csrfFormMiddleware(logoutHandler(opts)))
	router.Handle("/", sessionAuthMiddleware(opts, bundleFS, authedMux))
	return router
}

func authControllerURL(opts HandlerOptions) string {
	if opts.AuthControllerURL != "" {
		return opts.AuthControllerURL
	}
	return opts.ControllerURL
}

func validateAuthOptions(opts HandlerOptions) error {
	if !opts.RequireLogin {
		return nil
	}
	raw := authControllerURL(opts)
	if raw == "" {
		return errors.New("login-required mode needs a controller session backend")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(raw) != raw {
		return fmt.Errorf("controller session backend must be an absolute http(s) URL without credentials, query, or fragment: %q", raw)
	}
	return nil
}

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

		// hack: prefer <route>.html because Next 16 emits a same-named Turbopack
		// directory that FileServer redirects into a dead end.
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

func isTemplatedPath(p string) bool {
	return !strings.HasPrefix(p, "_next/") && !strings.HasPrefix(p, "next-dev/")
}

func serveTemplatedHTML(w http.ResponseWriter, _ *http.Request, bundleFS fs.FS, name string, opts HandlerOptions) {
	raw, err := fs.ReadFile(bundleFS, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	effectiveLogin := loginRequired(opts)
	frontendToken := opts.Token
	frontendAPIURL := opts.APIURL
	if effectiveLogin {
		frontendToken = ""
		frontendAPIURL = ""
	}
	body := bytes.ReplaceAll(raw,
		[]byte("__SPARKWING_TOKEN_MARKER__"),
		[]byte(jsStringEscape(frontendToken)))
	body = bytes.ReplaceAll(body,
		[]byte("__SPARKWING_API_URL_MARKER__"),
		[]byte(jsStringEscape(frontendAPIURL)))
	body = bytes.ReplaceAll(body,
		[]byte("__SPARKWING_VERSION_MARKER__"),
		[]byte(jsStringEscape(opts.Version)))
	body = bytes.ReplaceAll(body,
		[]byte("__SPARKWING_REQUIRE_LOGIN_MARKER__"),
		[]byte(strconv.FormatBool(effectiveLogin)))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

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
		case '<': // safety: prevents injection that breaks out of the surrounding <script> tag
			b.WriteString(`<`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func controllerProxy(controllerURL, token string, loginRequired bool) http.Handler {
	u, err := url.Parse(controllerURL)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, fmt.Sprintf("bad controller URL: %v", err), http.StatusInternalServerError)
		})
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del(csrfHeaderName)
			if loginRequired {
				pr.Out.Header.Del("Authorization")
				pr.Out.Header.Del("Proxy-Authorization")
			}
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

func serveLogStream(b backend.Backend, w http.ResponseWriter, r *http.Request, runID, nodeID string) {
	body, err := b.StreamNodeLog(r.Context(), runID, nodeID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if body == nil {
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

func serveEventsStream(b backend.Backend, w http.ResponseWriter, r *http.Request, runID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}

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
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte(": open\n\n"))
	flusher.Flush()

	ctx := r.Context()
	const (
		pollInterval    = 250 * time.Millisecond
		pageSize        = 500
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

		if terminal && len(events) == 0 {
			_, _ = w.Write([]byte("event: stream_end\ndata: {}\n\n"))
			flusher.Flush()
			return
		}

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

func isRunTerminal(status string) bool {
	switch status {
	case "success", "failed", "cancelled":
		return true
	}
	return false
}

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

type displayLine struct {
	body string
	step string
	show bool
}

func displayBodyForLogLine(raw string) (string, bool) {
	d := parseDisplayLine(raw)
	return d.body, d.show
}

func parseDisplayLine(raw string) displayLine {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed[0] != '{' {
		return displayLine{body: raw, show: true}
	}
	var rec struct {
		Event string                 `json:"event"`
		Msg   string                 `json:"msg"`
		Level string                 `json:"level"`
		Step  string                 `json:"step"`
		Attrs map[string]interface{} `json:"attrs,omitempty"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return displayLine{body: raw, show: true}
	}
	switch rec.Event {
	case "node_start", "step_start", "step_end", "node_end", "run_summary":
		return displayLine{show: false}
	case "step_skipped":
		reason, _ := rec.Attrs["reason"].(string)
		if reason != "" {
			return displayLine{body: "[skipped: " + reason + "]", step: rec.Step, show: true}
		}
		return displayLine{body: "[skipped]", step: rec.Step, show: true}
	}
	if rec.Msg != "" {
		return displayLine{body: rec.Msg, step: rec.Step, show: true}
	}
	if len(rec.Attrs) > 0 {
		attrBytes, _ := json.Marshal(rec.Attrs)
		return displayLine{body: string(attrBytes), step: rec.Step, show: true}
	}
	return displayLine{body: "", step: rec.Step, show: true}
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
		// perf: fan out per-node reads; each ReadNodeLog is a separate HTTP hop in cluster mode.
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

func runsGrepHandler(b backend.Backend) http.HandlerFunc {
	type match struct {
		RunID    string `json:"run_id"`
		Pipeline string `json:"pipeline"`
		NodeID   string `json:"node_id"`
		StepID   string `json:"step_id,omitempty"`
		Line     int    `json:"line"`
		Content  string `json:"content"`
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
					d := parseDisplayLine(sc.Text())
					if !d.show {
						continue
					}
					displayLine++
					if !strings.Contains(strings.ToLower(d.body), needle) {
						continue
					}
					local.count++
					if maxMatches == 0 || local.count <= maxMatches {
						local.matches = append(local.matches, match{
							RunID:    u.run.ID,
							Pipeline: u.run.Pipeline,
							NodeID:   u.nodeID,
							StepID:   d.step,
							Line:     displayLine,
							Content:  d.body,
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
		runsMeta := make(map[string]*store.Run, len(hitRuns))
		for id := range hitRuns {
			if run := runIndex[id]; run != nil {
				runsMeta[id] = store.RedactedRun(run)
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
