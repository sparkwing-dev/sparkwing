package wingd

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
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
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "controller lifecycle requires a verified local socket peer"})
			return
		}
		if _, err := LocalSubject(peer, owner); err != nil {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: err.Error()})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, controllerHTTPPath)
		method := http.MethodPost
		if path == "status" || path == "probe" {
			method = http.MethodGet
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "use " + method})
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
				writeLoginFailure(w, &apierr.Error{Code: apierr.Unavailable, Message: "controller runtime is unavailable"})
				return
			}
			writeControllerHTTP(w, d.localControl())
		case "dashboard":
			if d.localDashboard == nil {
				writeLoginFailure(w, &apierr.Error{Code: apierr.Unavailable, Message: "controller dashboard is unavailable"})
				return
			}
			var request wingwire.LocalDashboard
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil || request.Address == "" {
				writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "dashboard address is required"})
				return
			}
			reply := wingwire.LocalDashboardAck{ReadOnly: request.ReadOnly}
			if err := d.localDashboard(r.Context(), request.Address, request.ReadOnly); err != nil {
				reply.Error = err.Error()
			}
			writeControllerHTTP(w, reply)
		case "drain":
			d.recordJournal("drain_begin", nil, nil)
			remaining, err := d.beginDrain()
			defer func() {
				// safety: deliver even a failed drain acknowledgement before cancelling HTTP handlers.
				if flush, ok := w.(http.Flusher); ok {
					flush.Flush()
				}
				d.shutdownWithReason("drain")
			}()
			if err != nil {
				writeLoginFailure(w, &apierr.Error{Code: apierr.Internal, Message: err.Error()})
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
