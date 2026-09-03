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
// held store. It matches the write timeout a hosted controller applies, so a
// caller wedged behind a foreign writer gets an error it can retry instead of
// a connection that never answers.
const APIRequestTimeout = 30 * time.Second

const apiAuthCacheTTL = time.Minute

// PeerPrincipalPrefix names a caller authenticated by the uid the kernel
// reported for its api.sock connection rather than by a token. Audit records
// carry it in place of a principal name.
const PeerPrincipalPrefix = "unix-peer:"

type peerUIDKey struct{}

// wingdAPI serves the controller HTTP API on the daemon's held store. The
// store opens lazily and can be replaced under the daemon, so the router is
// rebuilt whenever the handle it was built on changes.
type wingdAPI struct {
	runs     *HeldRunStore
	artifact storage.ArtifactStore
	logger   *slog.Logger

	mu      sync.Mutex
	builtOn *store.Store
	handler http.Handler
}

// apiReadRoutes are the routes served from the read-only handle. A SQLite
// store is one connection, so a read behind a foreign writer's transaction
// waits it out on the writing handle while a WAL reader does not: measured
// against a four-second foreign write, trigger polls went from a 3.1s p99 on
// the writing handle to the low milliseconds. The list is an allow-list
// rather than "every GET" because several GET routes write, and a route
// missing from it costs latency under contention, never correctness.
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

func newWingdAPI(runs *HeldRunStore, artifact storage.ArtifactStore, logger *slog.Logger) *wingdAPI {
	if logger == nil {
		logger = loopbackLogger()
	}
	return &wingdAPI{runs: runs, artifact: artifact, logger: logger}
}

func (a *wingdAPI) serve(ctx context.Context, ln net.Listener) {
	srv := &http.Server{
		Handler:           http.HandlerFunc(a.route),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       APIRequestTimeout,
		WriteTimeout:      APIRequestTimeout,
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
	if err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		a.logger.Warn("controller API stopped", "err", err)
	}
}

func (a *wingdAPI) route(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), APIRequestTimeout)
	defer cancel()
	rw, ro, err := a.runs.Create(ctx)
	if err != nil {
		writeAPIUnavailable(w, err)
		return
	}
	a.handlerFor(rw, ro).ServeHTTP(w, r.WithContext(ctx))
}

func writeAPIUnavailable(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, "{%q:%q,%q:%q}\n", "error", "unavailable", "message", err.Error())
}

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
	mux := http.NewServeMux()
	mux.Handle("/", a.controllerOn(rw, rw))
	if ro == nil {
		return mux
	}
	// safety: the authenticator keeps the writing handle because a bearer
	// token's own bookkeeping is a write, and a read route must not fail on
	// the header a CLI verb may send.
	read := a.controllerOn(ro, rw)
	for _, route := range apiReadRoutes {
		mux.Handle(route, read)
	}
	return mux
}

func (a *wingdAPI) controllerOn(st, auth *store.Store) http.Handler {
	return controller.New(st, a.logger).
		WithArtifactStore(a.artifact).
		WithAuthenticator(controller.NewAuthenticator(auth, apiAuthCacheTTL).WithLogger(a.logger)).
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
