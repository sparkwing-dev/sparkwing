// Package localws is the single-process local dev server: one HTTP
// server, one SQLite file, one port. Composes the controller,
// logs-service, and web handlers on the same mux so the CLI and the
// dashboard read from the same state.
package localws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/local"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/logs"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// Options configures the local dev server. Addr defaults to
// 127.0.0.1:4343; Home defaults to $SPARKWING_HOME or ~/.sparkwing.
type Options struct {
	Addr string
	Home string

	// Listener, when non-nil, supersedes Addr: Run serves on this
	// pre-built listener and takes ownership of closing it. Lets
	// callers (chiefly parallel tests) reserve a port and hand it
	// over without a close-then-rebind window where another process
	// can race in. The bound address is still reported via Addr's
	// usual channels (dev.env, baseURL) — callers should set Addr to
	// the listener's address for those side effects to be correct.
	Listener net.Listener

	// LogStore, when non-nil, routes dashboard log reads through this
	// backend instead of the default filesystem reader rooted at Home.
	LogStore storage.LogStore
	// LogStoreLabel is a short tag ("fs", "s3", ...) surfaced on
	// /api/v1/capabilities. Empty when LogStore is nil.
	LogStoreLabel string

	// ArtifactStore, when non-nil, exposes /api/v1/artifacts/{key}
	// and feeds the capabilities endpoint.
	ArtifactStore      storage.ArtifactStore
	ArtifactStoreLabel string

	// ReadOnly, when true, rejects state-mutating methods on every
	// /api/v1/* path except /api/v1/auth/* with 405. Auth stays open
	// so operators can still log in to a read-only console.
	ReadOnly bool

	// NoLocalStore, when true, skips opening the local SQLite store
	// and routes the dashboard's runs list through ArtifactStore
	// instead. Requires LogStore + ArtifactStore to be set. Implies a
	// read-only experience: the controller is not mounted, so write
	// endpoints are absent rather than 405.
	NoLocalStore bool
}

// Run starts the local dev server and blocks until ctx is cancelled
// or the HTTP server returns. Installs its own SIGINT/SIGTERM handler
// for standalone use; redundant when the parent ctx already cancels
// on signal.
func Run(ctx context.Context, opts Options) error {
	if opts.Listener != nil {
		// Mirror Addr from the listener so dev.env + baseURL stay
		// consistent even if the caller didn't set Addr explicitly.
		opts.Addr = opts.Listener.Addr().String()
	}
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:4343"
	}

	// Resolve SPARKWING_HOME before anything opens files so cooperating
	// processes share the same state dir.
	if opts.Home != "" {
		if err := os.Setenv("SPARKWING_HOME", opts.Home); err != nil {
			return err
		}
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return fmt.Errorf("resolve sparkwing home: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure %s: %w", paths.Root, err)
	}

	// S3-only mode: skip SQLite + the controller mount, read run state
	// from the artifact store. Write surface is absent because there's
	// no orchestrator behind it.
	useS3OnlyReader := opts.NoLocalStore && opts.LogStore != nil && opts.ArtifactStore != nil

	var st *store.Store
	if !useS3OnlyReader {
		s, err := store.Open(paths.StateDB())
		if err != nil {
			return fmt.Errorf("open %s: %w", paths.StateDB(), err)
		}
		st = s
		defer st.Close()
	}

	// Skipped when an external LogStore is configured: reads come from
	// that store and writes are the worker's responsibility.
	var logsSrv *logs.Server
	if opts.LogStore == nil {
		var err error
		logsSrv, err = logs.New(paths.Root, nil)
		if err != nil {
			return fmt.Errorf("logs server: %w", err)
		}
	}

	var ctrl *local.Server
	if !useS3OnlyReader {
		ctrl = local.New(st, nil)
		if opts.ArtifactStore != nil {
			ctrl.SetArtifactStore(opts.ArtifactStore)
		}
	}

	var dashBackend backend.Backend
	if useS3OnlyReader {
		s3b := backend.NewS3Backend(opts.ArtifactStore, opts.LogStore)
		s3b.SetCapabilities(backend.Capabilities{
			Mode:     "s3-only",
			Storage:  backendCapabilitiesStorage(opts, "s3"),
			Features: []string{"pipelines", "runs", "logs"},
			ReadOnly: true,
		})
		dashBackend = s3b
	} else {
		sb := backend.NewStoreBackend(st, paths, opts.LogStore)
		sb.SetCapabilities(backend.Capabilities{
			Mode:     "local",
			Storage:  backendCapabilitiesStorage(opts, "sqlite"),
			Features: localFeatures(),
		})
		dashBackend = sb
	}

	webOpts := web.HandlerOptions{
		Backend: dashBackend,
		Paths:   paths,
	}
	webHandler := web.HandlerFromOptions(webOpts)

	// Go 1.22 ServeMux picks the most specific pattern. The
	// dashboard-owned /api/v1/runs/{id}/{logs,events} patterns must
	// be registered alongside the controller's catch-all /api/v1/ —
	// see TestMuxSpecificity_ApiV1Routing for the load-bearing
	// assertion.
	root := http.NewServeMux()
	root.Handle("/api/v1/health/services", webHandler)
	root.Handle("GET /api/v1/runs/grep", webHandler)
	root.Handle("GET /api/v1/runs/{id}/logs", webHandler)
	root.Handle("GET /api/v1/runs/{id}/logs/{node}", webHandler)
	root.Handle("GET /api/v1/runs/{id}/logs/{node}/stream", webHandler)
	root.Handle("GET /api/v1/runs/{id}/events/stream", webHandler)
	root.Handle("GET /api/v1/capabilities", web.CapabilitiesHandler(dashBackend))
	if useS3OnlyReader {
		root.Handle("GET /api/v1/runs", web.ListRunsHandler(dashBackend))
		root.Handle("GET /api/v1/runs/{id}", web.GetRunHandler(dashBackend))
	}
	if logsSrv != nil {
		root.Handle("/api/v1/logs/", logsSrv.Handler())
	}
	if ctrl != nil {
		ctrlHandler := ctrl.Handler()
		if opts.ReadOnly {
			ctrlHandler = readOnlyMiddleware(ctrlHandler)
		}
		root.Handle("/api/v1/", ctrlHandler)
		root.Handle("/webhooks/", ctrlHandler)
	} else {
		// Local-only mode has no controller and therefore no
		// pipelines.yaml registry. The dashboard polls
		// /api/v1/pipelines for tag-filter options and registry-only
		// rows; without a stub the requests 404 every poll cycle.
		// Empty body is the right answer — the UI degrades gracefully
		// (no tag options, no unrun pipelines) and the controller's
		// real handler wins when present via Go 1.22 mux specificity.
		root.Handle("GET /api/v1/pipelines",
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"pipelines":{}}`))
			}))
	}
	root.Handle("/", webHandler)

	// Export the base URL so cooperating processes can find us
	// without a hardcoded port.
	baseURL := "http://" + opts.Addr
	if err := writeDevEnv(paths.Root, baseURL); err != nil {
		return fmt.Errorf("write dev.env: %w", err)
	}

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// SSE log-stream endpoint holds writes open for the lifetime
		// of a run; a low WriteTimeout would cut tailing mid-run.
		IdleTimeout: 2 * time.Minute,
	}

	// One Listen up-front: either reuse the caller's listener (no
	// close-then-rebind window for another process to slip in on the
	// same port) or open one ourselves. srv.Serve takes ownership and
	// Shutdown will close it.
	lis := opts.Listener
	if lis == nil {
		l, err := net.Listen("tcp", opts.Addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", opts.Addr, err)
		}
		lis = l
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(lis)
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

// backendCapabilitiesStorage builds the storage portion of the
// /api/v1/capabilities response. Defaults to "fs" for logs+artifacts
// when no override store is wired.
func backendCapabilitiesStorage(opts Options, runs string) backend.CapabilitiesStorage {
	out := backend.CapabilitiesStorage{Artifacts: "fs", Logs: "fs", Runs: runs}
	if opts.LogStore != nil {
		out.Logs = nonEmpty(opts.LogStoreLabel, "custom")
	}
	if opts.ArtifactStore != nil {
		out.Artifacts = nonEmpty(opts.ArtifactStoreLabel, "custom")
	}
	return out
}

// localFeatures is the local-mode feature flag list surfaced on
// /api/v1/capabilities.
func localFeatures() []string {
	return []string{
		"pipelines", "runs", "logs",
		"secrets", "approvals", "cross-pipeline-refs",
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// readOnlyMiddleware rejects state-mutating methods on /api/v1/*
// except /api/v1/auth/* and /webhooks/* (webhook senders can't honor
// a 405).
func readOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") ||
			strings.HasPrefix(r.URL.Path, "/webhooks/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "read-only mode: writes disabled", http.StatusMethodNotAllowed)
	})
}

// writeDevEnv records the base URL at $SPARKWING_HOME/dev.env so
// cooperating processes can reach us. Overwritten each startup.
func writeDevEnv(root, baseURL string) error {
	body := fmt.Sprintf("SPARKWING_CONTROLLER_URL=%s\nSPARKWING_LOGS_URL=%s\n", baseURL, baseURL)
	return os.WriteFile(filepath.Join(root, "dev.env"), []byte(body), 0o644)
}
