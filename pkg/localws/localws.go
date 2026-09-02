package localws

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const schemaPollInterval = 5 * time.Second

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
	// usual channels (dev.env, baseURL) -- callers should set Addr to
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

	// AllowRemote lets the server bind a non-loopback Addr and answer
	// requests whose Host is not loopback. The API carries no
	// authentication, so this hands every reachable network the power
	// to run pipelines and read secrets. Off by default; Run refuses a
	// non-loopback Addr without it.
	AllowRemote bool

	// AllowOrigins lists browser origins ("https://dash.example") whose
	// requests the API answers in addition to loopback ones. Only useful
	// with AllowRemote, where the request Host no longer proves the caller
	// reached this process over the loopback interface.
	AllowOrigins []string

	// Version is rendered as a small pill in the dashboard nav. The
	// caller passes the running CLI's version (typically the value
	// of cmd/sparkwing.installedVersion()). Empty hides the pill.
	Version string
}

// Run starts the local dev server and blocks until ctx is cancelled
// or the HTTP server returns. Installs its own SIGINT/SIGTERM handler
// for standalone use; redundant when the parent ctx already cancels
// on signal.
func Run(ctx context.Context, opts Options) error {
	if opts.Listener != nil {
		opts.Addr = opts.Listener.Addr().String()
	}
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:4343"
	}
	if !opts.AllowRemote && !LoopbackBind(opts.Addr) {
		return fmt.Errorf("addr %s is not loopback: set AllowRemote to serve the unauthenticated API to other hosts", opts.Addr)
	}
	if err := web.VerifyBundleEmbedded(); err != nil {
		return err
	}

	paths, err := localPaths(opts.Home)
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure %s: %w", paths.Root, err)
	}

	useS3OnlyReader := opts.NoLocalStore && opts.LogStore != nil && opts.ArtifactStore != nil

	var st *store.Store
	if !useS3OnlyReader {
		s, err := store.Open(paths.StateDB())
		if err != nil {
			return fmt.Errorf("open %s: %w", paths.StateDB(), err)
		}
		st = s
		defer func() { _ = st.Close() }()
	}

	var logsSrv *logs.Server
	if opts.LogStore == nil {
		var err error
		logsSrv, err = logs.NewPrivate(paths.Root, nil)
		if err != nil {
			return fmt.Errorf("logs server: %w", err)
		}
	}

	var ctrl *controller.Server
	if !useS3OnlyReader {
		ctrl = controller.New(st, nil).
			WithArtifactStore(opts.ArtifactStore).
			WithReconcileHook(func(rctx context.Context) error {
				_, err := orchestrator.ReconcileOrphanedLocalRuns(rctx, st, 0)
				return err
			})
		if err := orchestrator.RunLocalTriggerConsumer(ctx, paths.Root, st, nil); err != nil {
			return err
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

	baseURL := "http://" + opts.Addr
	if err := writeDevEnv(paths.Root, baseURL); err != nil {
		return fmt.Errorf("write dev.env: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	handler := buildHandler(ctx, cancel, opts, handlerParts{
		paths:        paths,
		backend:      dashBackend,
		store:        st,
		ctrl:         ctrl,
		logs:         logsSrv,
		s3OnlyReader: useS3OnlyReader,
	}, web.BundleFS())

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	lis := opts.Listener
	if lis == nil {
		l, err := net.Listen("tcp", opts.Addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", opts.Addr, err)
		}
		lis = l
	}

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

type handlerParts struct {
	paths        orchestrator.Paths
	backend      backend.Backend
	store        *store.Store
	ctrl         *controller.Server
	logs         *logs.Server
	s3OnlyReader bool
}

func buildHandler(
	ctx context.Context,
	cancel context.CancelFunc,
	opts Options,
	parts handlerParts,
	bundle fs.FS,
) http.Handler {
	webOpts := web.HandlerOptions{
		Backend: parts.backend,
		Paths:   parts.paths,
		Version: opts.Version,
	}
	webHandler := web.HandlerFromOptionsWithBundle(webOpts, bundle)

	root := http.NewServeMux()
	root.Handle("GET /api/v1/version", versionHandler(opts.Version))
	root.Handle("/api/v1/health/services", webHandler)
	root.Handle("GET /api/v1/runs/grep", webHandler)
	root.Handle("GET /api/v1/runs/{id}/logs", webHandler)
	root.Handle("GET /api/v1/runs/{id}/logs/{node}", webHandler)
	root.Handle("GET /api/v1/runs/{id}/logs/{node}/stream", webHandler)
	root.Handle("GET /api/v1/runs/{id}/events/stream", webHandler)
	root.Handle("GET /api/v1/capabilities", web.CapabilitiesHandler(parts.backend))
	if parts.s3OnlyReader {
		root.Handle("GET /api/v1/runs", web.ListRunsHandler(parts.backend))
		root.Handle("GET /api/v1/runs/{id}", web.GetRunHandler(parts.backend))
	}
	if parts.logs != nil {
		root.Handle("/api/v1/logs/", parts.logs.Handler())
	}
	root.Handle("GET /api/v1/pipelines", aggregatedPipelinesHandler())
	root.Handle("GET /api/v1/queue", queueHandler(parts.paths.Root, opts.Version))
	// safety: the controller claims all of /api/v1/, so dashboard-owned routes
	// must be named here to remain reachable.
	root.Handle("GET /api/v1/capacity/profiles", webHandler)
	root.Handle("GET /api/v1/capacity/profiles/explain", webHandler)
	if parts.ctrl != nil {
		ctrlHandler := parts.ctrl.Handler()
		if opts.ReadOnly {
			ctrlHandler = readOnlyMiddleware(ctrlHandler)
		}
		root.Handle("/api/v1/", ctrlHandler)
		root.Handle("/webhooks/", ctrlHandler)
	}
	root.Handle("/", webHandler)

	var handler http.Handler = root
	if parts.store != nil {
		guard := newSchemaGuard(parts.store, cancel)
		handler = guard.middleware(root)
		go guard.poll(ctx, schemaPollInterval)
	}
	return originGuard(handler, opts.originPolicy())
}

func (o Options) originPolicy() originPolicy {
	return originPolicy{
		allowRemote:  o.AllowRemote,
		bindHost:     bindOriginHost(o.Addr),
		allowOrigins: o.AllowOrigins,
	}
}

func localPaths(home string) (orchestrator.Paths, error) {
	if home != "" {
		return orchestrator.PathsAt(home), nil
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return orchestrator.Paths{}, fmt.Errorf("resolve sparkwing home: %w", err)
	}
	return paths, nil
}

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

func writeDevEnv(root, baseURL string) error {
	body := fmt.Sprintf("SPARKWING_CONTROLLER_URL=%s\nSPARKWING_LOGS_URL=%s\n", baseURL, baseURL)
	return fssecure.WriteFile(filepath.Join(root, "dev.env"), []byte(body))
}
