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
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	// usual channels (dev.env, baseURL) -- callers should set Addr to
	// the listener's address for those side effects to be correct.
	Listener net.Listener

	// ReadOnly, when true, rejects state-mutating methods on every
	// /api/v1/* path except /api/v1/auth/* with 405. Auth stays open
	// so operators can still log in to a read-only console.
	ReadOnly bool

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

	st, err := store.Open(paths.StateDB()) //nolint:contextcheck // Store.Open owns its bounded migration context and has no caller-context variant.
	if err != nil {
		return fmt.Errorf("open %s: %w", paths.StateDB(), err)
	}
	defer func() { retErr = errors.Join(retErr, st.Close()) }()

	ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{})
	if err != nil {
		return fmt.Errorf("local secrets key: %w", err)
	}
	ctrl := controller.New(st, nil).
		WithSecretsCipher(ring.For(st)).
		WithDashboard(controller.Dashboard{
			Local:   true,
			Bundle:  bundle,
			Version: opts.Version,
			Paths:   paths,
			Capabilities: backend.Capabilities{
				Mode:     "local",
				Storage:  backend.CapabilitiesStorage{Artifacts: "fs", Logs: "fs", Runs: "sqlite"},
				Features: localFeatures(),
			},
		})
	if err := orchestrator.RunLocalTriggerConsumer(ctx, paths.Root, st, nil, opts.Version); err != nil {
		return err
	}

	baseURL := "http://" + opts.Addr
	if err := writeDevEnv(paths.Root, baseURL); err != nil {
		return fmt.Errorf("write dev.env: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	handler := buildHandler(opts, handlerParts{
		paths:      paths,
		store:      st,
		ctrl:       ctrl,
		serveToken: serveToken,
	})

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
	paths      orchestrator.Paths
	store      *store.Store
	ctrl       *controller.Server
	serveToken string
}

func buildHandler(opts Options, parts handlerParts) http.Handler {
	root := http.NewServeMux()
	root.Handle("GET /api/v1/version", versionHandler(opts.Version, opts.Instance))
	root.Handle("GET /api/v1/pipelines", aggregatedPipelinesHandler())
	root.Handle("GET /api/v1/queue", queueHandler(parts.paths.Root, opts.Version))
	registerCronRoutes(root, &cronsAPI{store: parts.store, paths: parts.paths, readOnly: opts.ReadOnly})
	// safety: this API has no sign-in, so a masked value never leaves it;
	// the dashboard lists, writes and deletes rows without reading one.
	root.Handle("GET /api/v1/secrets/{name}", http.NotFoundHandler())
	root.Handle("/", parts.ctrl.Handler())

	var handler http.Handler = root
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

func localFeatures() []string {
	return []string{
		"pipelines", "runs", "logs",
		"secrets", "approvals", "cross-pipeline-refs",
	}
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
	body := fmt.Sprintf("SPARKWING_CONTROLLER_URL=%s\n", baseURL)
	return fssecure.WriteFile(filepath.Join(root, "dev.env"), []byte(body))
}
