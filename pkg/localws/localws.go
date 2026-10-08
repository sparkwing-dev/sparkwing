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
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/originguard"
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

	// ArtifactStore, when non-nil, feeds the capabilities endpoint and
	// the bucket ceiling's measurement.
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
	// requests whose Host is not loopback. Hosts holding the serve token
	// can run pipelines and access local secrets. Off by default; Run
	// refuses a non-loopback Addr without it.
	AllowRemote bool

	// AllowOrigins lists browser origins ("https://dash.example") whose
	// requests the API answers in addition to loopback ones, as Origin and
	// as Host: the public name of a proxy on this host, or under
	// AllowRemote a name that is not the Addr host.
	AllowOrigins []string

	// Version is rendered as a small pill in the dashboard nav. The
	// caller passes the running CLI's version (typically the value
	// of cmd/sparkwing.installedVersion()). Empty hides the pill.
	Version string

	// Instance binds a lifecycle readiness probe to this supervisor invocation.
	Instance string

	// Bundle, when non-nil, is served as the dashboard in place of the
	// bundle embedded in this binary, rooted at its index.html the way
	// web.BundleFS returns it. A source build carries no embedded bundle,
	// so a test that serves the dashboard supplies its own.
	Bundle fs.FS
}

// Run starts the local dev server and blocks until ctx is cancelled
// or the HTTP server returns. Installs its own SIGINT/SIGTERM handler
// for standalone use; redundant when the parent ctx already cancels
// on signal.
func Run(ctx context.Context, opts Options) (retErr error) {
	if opts.Listener != nil {
		defer func() {
			if err := opts.Listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				retErr = errors.Join(retErr, err)
			}
		}()
		opts.Addr = opts.Listener.Addr().String()
	}
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:4343"
	}
	if !opts.AllowRemote && !LoopbackBind(opts.Addr) {
		return fmt.Errorf("addr %s is not loopback: set AllowRemote to allow network access", opts.Addr)
	}
	if opts.AllowRemote && !LoopbackBind(opts.Addr) {
		fmt.Fprintf(os.Stderr, "sparkwing serve: WARNING: %s is not loopback, so every host that reaches it can try "+
			"the serve token, and anyone holding it can list, overwrite and delete this machine's local secrets and runs\n", opts.Addr)
	}
	bundle := opts.Bundle
	if bundle == nil {
		if err := web.VerifyBundleEmbedded(); err != nil {
			return err
		}
		bundle = web.BundleFS()
	} else if err := web.VerifyBundle(bundle); err != nil {
		return err
	}

	paths, err := localPaths(opts.Home)
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure %s: %w", paths.Root, err)
	}
	serveToken, err := paths.EnsureServeToken()
	if err != nil {
		return fmt.Errorf("serve token: %w", err)
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
		ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{})
		if err != nil {
			return fmt.Errorf("local secrets key: %w", err)
		}
		ctrl = controller.New(st, nil).
			WithArtifactStore(opts.ArtifactStore).
			WithSecretsCipher(ring.For(st)).
			WithLocalExecution().
			WithReconcileHook(func(rctx context.Context) error {
				_, err := orchestrator.ReconcileOrphanedLocalRuns(rctx, st, 0)
				return err
			}).
			WithDashboard(controller.Dashboard{
				Local:   true,
				Bundle:  bundle,
				Version: opts.Version,
				Logs:    opts.LogStore,
				Paths:   paths,
				Capabilities: backend.Capabilities{
					Mode:     "local",
					Storage:  backendCapabilitiesStorage(opts, "sqlite"),
					Features: localFeatures(),
				},
			})
		if err := orchestrator.RunLocalTriggerConsumer(ctx, paths.Root, st, nil, opts.Version); err != nil {
			return err
		}
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
		serveToken:   serveToken,
	}, bundle)

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
	serveToken   string
}

func buildHandler(
	ctx context.Context,
	cancel context.CancelFunc,
	opts Options,
	parts handlerParts,
	bundle fs.FS,
) http.Handler {
	root := http.NewServeMux()
	root.Handle("GET /api/v1/version", versionHandler(opts.Version, opts.Instance))
	root.Handle("GET /api/v1/pipelines", aggregatedPipelinesHandler())
	root.Handle("GET /api/v1/queue", queueHandler(parts.paths.Root, opts.Version))
	registerCronRoutes(root, &cronsAPI{store: parts.store, paths: parts.paths, readOnly: opts.ReadOnly})
	if parts.logs != nil {
		root.Handle("/api/v1/logs/", parts.logs.Handler())
	}
	if parts.ctrl != nil {
		// safety: this API has no sign-in, so a masked value never leaves it;
		// the dashboard lists, writes and deletes rows without reading one.
		root.Handle("GET /api/v1/secrets/{name}", http.NotFoundHandler())
		root.Handle("/", parts.ctrl.Handler())
	} else {
		registerObjectStoreDashboard(root, parts.backend, opts.Version, bundle)
	}

	var handler http.Handler = root
	if parts.store != nil {
		guard := newSchemaGuard(parts.store, cancel)
		handler = guard.middleware(root)
		go guard.poll(ctx, schemaPollInterval)
	}
	if opts.ReadOnly {
		handler = readOnlyMiddleware(handler)
	}
	// safety: the sign-in exchange lives in the token gate, so the gate sits
	// inside the Host and Origin guard or a rebound page could trade a code.
	if parts.serveToken != "" {
		handler = requireServeToken(handler, parts.serveToken)
	}
	return web.SecurityHeaders(false,
		originguard.Guard(handler, originguard.NewPolicy(opts.Addr, opts.AllowRemote, opts.AllowOrigins)))
}

// safety: with no local store there is no controller, so the object-store reader answers the dashboard's reads
// itself; it has no write route to offer.
func registerObjectStoreDashboard(mux *http.ServeMux, b backend.Backend, version string, bundle fs.FS) {
	mux.Handle("GET /api/v1/capabilities", web.CapabilitiesHandler(b))
	mux.Handle("GET /api/v1/runs", web.ListRunsHandler(b))
	mux.Handle("GET /api/v1/runs/{id}", web.GetRunHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/logs", web.RunLogsHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/logs/search", web.RunLogsSearchHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}", web.NodeLogsHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}/stream", web.NodeLogStreamHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}/completeness", web.NodeLogCompletenessHandler(b))
	mux.Handle("GET /api/v1/runs/grep", web.RunsGrepHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/events/stream", web.EventsStreamHandler(b))
	mux.Handle("GET /sparkwing-runtime.js", web.RuntimeConfig(version, false))
	mux.Handle("/api/", http.NotFoundHandler())
	mux.Handle("/", web.Pages(bundle))
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
