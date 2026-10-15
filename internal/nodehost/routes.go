// Package nodehost executes SDK processes and serves their bound host services.
package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// Server serves the /node/v1 routes on a loopback port. Each node reaches it
// with its own bearer from [Server.Admit], which confines every request to
// that node's run and node.
type Server struct {
	listener net.Listener
	http     *http.Server
	logger   *slog.Logger
	ctx      context.Context
	cancel   context.CancelFunc

	mu     sync.RWMutex
	grants map[[bearerHashSize]byte]*grant

	cleanups cleanupLedger
}

// Start listens on an ephemeral 127.0.0.1 port and serves until Close. A nil
// logger discards the audit lines.
func Start(logger *slog.Logger) (*Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("node routes listen: %w", err)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{listener: listener, logger: logger, grants: map[[bearerHashSize]byte]*grant{}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	handler, err := s.routes()
	if err != nil {
		_ = listener.Close()
		s.cancel()
		return nil, err
	}
	s.http = &http.Server{
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return s.ctx },
		MaxHeaderBytes:    8 << 10,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      maxAwait + 5*time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("node routes stopped", "err", err)
		}
	}()
	return s, nil
}

// URL is the base URL a node process reaches the routes at.
func (s *Server) URL() string { return "http://" + s.listener.Addr().String() }

// Close stops the server and cancels every request in flight.
func (s *Server) Close() error {
	s.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

func (s *Server) routes() (http.Handler, error) {
	mux := http.NewServeMux()
	if err := RegisterProcessServices(serverRegistrar{s, mux}, func(ctx context.Context, _ capability.Authorized) (*ProcessServices, error) {
		g, _ := ctx.Value(serverGrantKey{}).(*grant)
		if g == nil {
			return nil, serviceUnavailable()
		}
		return g.services, nil
	}); err != nil {
		return nil, err
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, request *http.Request) {
		code := apierr.UnsupportedRoute
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			probe := request.Clone(request.Context())
			probe.Method = method
			if _, pattern := mux.Handler(probe); pattern != "/" && pattern != "" {
				code = apierr.MethodNotAllowed
				break
			}
		}
		s.writeFailure(w, executorError(code, "unsupported process service request"))
	})
	return mux, nil
}

type (
	serverGrantKey  struct{}
	serverRegistrar struct {
		server *Server
		mux    *http.ServeMux
	}
)

func (router serverRegistrar) Register(endpoint controller.Endpoint) error {
	router.mux.HandleFunc(endpoint.Method+" "+endpoint.Pattern, func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		g := router.server.authenticate(request)
		if g == nil {
			router.server.writeFailure(w, executorError(apierr.Unauthenticated, "a process bearer is required"))
			return
		}
		evidence, err := endpoint.Decode(request)
		if err == nil {
			auth := capability.Authorized{Binding: capability.Binding{Kind: capability.BindingNode, RunID: g.RunID, NodeID: g.NodeID}, Target: evidence.Target}
			request = request.WithContext(context.WithValue(request.Context(), serverGrantKey{}, g))
			err = endpoint.Handle(w, request, auth)
		}
		if err != nil {
			router.server.writeFailure(w, maskHostError(err, g.services.Masks.mask))
		}
	})
	return nil
}

func (s *Server) writeFailure(w http.ResponseWriter, err error) {
	var failure *apierr.Error
	if !errors.As(err, &failure) {
		failure = &apierr.Error{Code: apierr.Unavailable, Message: "process service is unavailable"}
	}
	s.writeJSON(w, apierr.Status(failure.Code), apierr.Envelope{Error: failure})
}

func routeFailure(code apierr.Code, reason apierr.Reason, message string) error {
	failure := &apierr.Error{Code: code, Message: message}
	if reason != "" {
		failure.Details = map[string]any{"reason": reason}
	}
	return failure
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.logger.Warn("node route reply not written", "status", status, "err", err)
	}
}
