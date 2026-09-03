package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// APIRequestTimeout bounds one controller API request against the daemon's
// held store, so a caller wedged behind a foreign writer gets an error it can
// retry instead of a connection that never answers. The streaming routes are
// exempt; they run to the deadline the handler sets for itself.
const APIRequestTimeout = 30 * time.Second

const apiAuthCacheTTL = time.Minute

// PeerPrincipalPrefix names a caller authenticated by the uid the kernel
// reported for its api.sock connection rather than by a token. Audit records
// carry it in place of a principal name.
const PeerPrincipalPrefix = "unix-peer:"

type peerUIDKey struct{}

type wingdAPI struct {
	runs           *HeldRunStore
	artifact       storage.ArtifactStore
	logger         *slog.Logger
	requestTimeout time.Duration

	mu      sync.Mutex
	builtOn *store.Store
	handler http.Handler
}

// perf: a SQLite store is one connection, so a read waits out a foreign
// writer on the writing handle and not on a WAL reader: against a four-second
// foreign write, trigger polls fell from a 3.1s p99 to milliseconds. An
// allow-list, not every GET, because several GET routes write.
var apiReadRoutes = []string{
	"GET /api/v1/health",
	"GET /api/v1/runs",
	"GET /api/v1/runs/{id}",
	"GET /api/v1/runs/{id}/nodes",
	"GET /api/v1/runs/{id}/nodes/{nodeID}",
	"GET /api/v1/runs/{id}/nodes/{nodeID}/output",
	"GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch",
	"GET /api/v1/runs/{id}/nodes/{nodeID}/dispatches",
	"GET /api/v1/runs/{id}/nodes/{nodeID}/metrics",
	"GET /api/v1/runs/{id}/nodes/{nodeID}/debug-pause",
	"GET /api/v1/runs/{id}/steps",
	"GET /api/v1/runs/{id}/events",
	"GET /api/v1/runs/{id}/approvals",
	"GET /api/v1/runs/{id}/approvals/{nodeID}",
	"GET /api/v1/runs/{id}/debug-pauses",
	"GET /api/v1/runs/{id}/paused",
	"GET /api/v1/approvals/pending",
	"GET /api/v1/triggers",
	"GET /api/v1/triggers/{id}",
	"GET /api/v1/triggers/spawned-child",
	"GET /api/v1/concurrency/{key}/state",
	"GET /api/v1/concurrency/{key}/holder",
	"GET /api/v1/pipelines/{name}/latest",
	"GET /api/v1/pipelines/{name}/profile",
}

// safety: these routes hold a response open far longer than a request bound
// would allow, and each sets its own deadline through http.ResponseController,
// so a context deadline here truncates them into a clean EOF the client reads
// as completion.
var apiStreamRoutes = []string{
	"GET /api/v1/concurrency/{key}/notify",
	"GET /api/v1/artifacts/{key}",
	"GET /api/v1/gitcache/git/{path...}",
	"POST /api/v1/gitcache/git/{path...}",
	"GET /api/v1/runs/{id}/gitcache/git/{path...}",
	"POST /api/v1/runs/{id}/gitcache/git/{path...}",
}

var apiStreamMux = routeSet(apiStreamRoutes)

func routeSet(routes []string) *http.ServeMux {
	mux := http.NewServeMux()
	marker := http.NotFoundHandler()
	for _, route := range routes {
		mux.Handle(route, marker)
	}
	return mux
}

func newWingdAPI(runs *HeldRunStore, artifact storage.ArtifactStore, logger *slog.Logger) *wingdAPI {
	if logger == nil {
		logger = loopbackLogger()
	}
	return &wingdAPI{runs: runs, artifact: artifact, logger: logger, requestTimeout: APIRequestTimeout}
}

func (a *wingdAPI) serve(ctx context.Context, ln net.Listener) {
	// safety: no write timeout, because the streaming routes extend the
	// connection's own deadline instead and a server-wide one would cut them;
	// the loopback controller this replaces for a local run does the same.
	srv := &http.Server{
		Handler:           http.HandlerFunc(a.route),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if peer, ok := c.(wingd.APIConn); ok {
				return context.WithValue(ctx, peerUIDKey{}, peer.PeerUID())
			}
			return ctx
		},
	}
	serving := make(chan struct{})
	defer close(serving)
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-serving:
		}
	}()
	err := srv.Serve(ln)
	// safety: Serve returns the moment the listener closes, with requests
	// still running against the store the host closes as soon as the daemon
	// returns, so the drain is what orders the API's shutdown against it.
	drainCtx, cancel := context.WithTimeout(context.Background(), wingd.APIDrainWindow)
	defer cancel()
	_ = srv.Shutdown(drainCtx)
	if a.unexpected(err) && ctx.Err() == nil {
		a.logger.Warn("controller API stopped", "err", err)
	}
}

// safety: a takeover closes the listener directly rather than through the
// server, so Serve reports a closed connection on the path that is working
// exactly as intended.
func (a *wingdAPI) unexpected(err error) bool {
	return err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed)
}

func (a *wingdAPI) route(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !streamingRoute(r) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.requestTimeout)
		defer cancel()
	}
	rw, ro, err := a.handles(ctx, r)
	if err != nil {
		writeAPIUnavailable(w, err)
		return
	}
	a.handlerFor(rw, ro).ServeHTTP(w, r.WithContext(ctx))
}

// safety: a run against an object-store profile must leave no local state
// behind, so only a request that changes state proves this home wants a store;
// a read, including the health probe, answers 503 rather than creating one.
func (a *wingdAPI) handles(ctx context.Context, r *http.Request) (*store.Store, *store.Store, error) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return a.runs.Handles(ctx)
	}
	return a.runs.Create(ctx)
}

func streamingRoute(r *http.Request) bool {
	_, pattern := apiStreamMux.Handler(r)
	return pattern != ""
}

func writeAPIUnavailable(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if !errors.Is(err, errRunStoreAbsent) {
		// safety: an absent store is answered rather than waited out, so only
		// a store that may yet open invites the client back.
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, "{%q:%q,%q:%q}\n", "error", "unavailable", "message", err.Error())
}

// safety: the held store opens lazily and is reopened when the file under it
// is replaced, so the router is rebuilt whenever the handle changes rather
// than pinned to the one the first request happened to find.
func (a *wingdAPI) handlerFor(rw, ro *store.Store) http.Handler {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.builtOn == rw && a.handler != nil {
		return a.handler
	}
	a.builtOn = rw
	a.handler = a.split(rw, ro)
	return a.handler
}

func (a *wingdAPI) split(rw, ro *store.Store) http.Handler {
	// safety: one authenticator for both servers, because each holds its own
	// token cache and its own revocation generation, and a revoke reaches only
	// the server that served it.
	auth := controller.NewAuthenticator(rw, apiAuthCacheTTL).WithLogger(a.logger)
	mux := http.NewServeMux()
	mux.Handle("/", a.controllerOn(rw, auth))
	if ro == nil {
		return mux
	}
	read := a.controllerOn(ro, auth)
	for _, route := range apiReadRoutes {
		mux.Handle(route, read)
	}
	return mux
}

// safety: the read server must never be given a reconcile hook. The two
// busiest read routes run it before answering, it writes, and the controller
// discards its error, so a hook here would fail silently on a read-only
// handle. The authenticator keeps the writing handle because a bearer token's
// own bookkeeping is a write.
func (a *wingdAPI) controllerOn(st *store.Store, auth *controller.Authenticator) http.Handler {
	return controller.New(st, a.logger).
		WithArtifactStore(a.artifact).
		WithAuthenticator(auth).
		WithPeerPrincipal(peerPrincipal).
		Handler()
}

// safety: the daemon refuses a connection from another account before the
// first byte is read, so a request that reached a handler is already from
// this uid; the principal exists to label audit records and to satisfy the
// handlers that read one, not to make a second authorization decision.
func peerPrincipal(r *http.Request) *controller.Principal {
	uid, ok := r.Context().Value(peerUIDKey{}).(int)
	if !ok {
		return nil
	}
	return &controller.Principal{
		Name:   PeerPrincipalPrefix + strconv.Itoa(uid),
		Kind:   "service",
		Scopes: []string{controller.ScopeAdmin},
		Authed: time.Now().UTC(),
	}
}
