package wingd

import (
	"encoding/json"
	"net/http"
	"strings"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const controllerHTTPPath = "/api/v1/local/controller/"

func (d *Daemon) controllerHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, controllerHTTPPath) {
			next.ServeHTTP(w, r)
			return
		}
		peer, verified := localhostwire.LocalPeer(r.Context())
		owner, err := controllerPeerIdentity()
		if !verifiedSocketPeer(r.Context()) || !verified || err != nil {
			writeLoginFailure(w, &apierr.Error{Code: hosterr.Unauthenticated, Message: "controller lifecycle requires a verified local socket peer"})
			return
		}
		if _, err := LocalSubject(peer, owner); err != nil {
			writeLoginFailure(w, &apierr.Error{Code: hosterr.Unauthenticated, Message: err.Error()})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, controllerHTTPPath)
		method := http.MethodPost
		if path == "status" || path == "probe" {
			method = http.MethodGet
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeLoginFailure(w, &apierr.Error{Code: hosterr.MethodNotAllowed, Message: "use " + method})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		switch path {
		case "probe":
			d.mu.Lock()
			draining := d.draining
			d.mu.Unlock()
			ack := wingwire.HelloAck{ProtocolMajor: ProtocolMajor, NativeProtocolMajor: ProtocolMajor, BinaryVersion: d.cfg.Version, BuildIdentity: wingwire.BuildIdentity, Draining: draining, StoreSchemaVersion: d.cfg.StoreSchemaVersion}
			ready, message := d.apiState()
			ack.APIReady, ack.APIError = &ready, message
			if d.cfg.Runs != nil {
				err := d.cfg.Runs.Ready()
				ready := err == nil
				ack.StoreReady = &ready
				if err != nil {
					ack.StoreError = err.Error()
				}
			}
			writeControllerHTTP(w, ack)
		case "status":
			if d.localControl == nil {
				writeLoginFailure(w, &apierr.Error{Code: hosterr.Unavailable, Message: "controller runtime is unavailable"})
				return
			}
			writeControllerHTTP(w, d.localControl())
		case "dashboard":
			// safety: a pre-step-5 CLI posts here to start the dashboard; name the cut instead of a bare 404.
			writeLoginFailure(w, &apierr.Error{
				Code:    hosterr.UnsupportedRoute,
				Message: "this local controller removed POST /api/v1/local/controller/dashboard; it serves the dashboard whenever it runs",
				Hint:    "run sparkwing dashboard open from a current CLI: sparkwing update for the installed one, sparkwing pipeline upgrade for a repository pin",
			})
		case "drain":
			d.recordJournal("drain_begin", nil, nil)
			remaining, err := d.beginDrain()
			retirementFailed := false
			if err == nil && r.URL.Query().Get("runner") == "retire" {
				err = d.retireLocalRunner(r.Context())
				retirementFailed = err != nil
			}
			defer func() {
				// safety: flush acknowledgements before cancelling their HTTP handlers.
				if flush, ok := w.(http.Flusher); ok {
					flush.Flush()
				}
				if !retirementFailed {
					d.shutdownWithReason("drain")
				}
			}()
			if err != nil {
				writeLoginFailure(w, &apierr.Error{Code: hosterr.Internal, Message: err.Error()})
				return
			}
			writeControllerHTTP(w, wingwire.DrainAck{HoldersRemaining: remaining})
		default:
			http.NotFound(w, r)
		}
	})
}

func writeControllerHTTP(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(http.ErrAbortHandler) //nolint:forbidigo // safety: net/http aborts an incomplete response.
	}
}
