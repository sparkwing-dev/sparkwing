// Package nodehost is the engine side of the node protocol: it runs the SDK
// process for a node and serves the node-facing /node/v1 routes that process
// calls. docs/node-protocol.md is the contract.
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

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
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
	mux := http.NewServeMux()
	s.handle(mux, "GET /node/v1/secrets/{name}", s.serveSecret)
	s.handle(mux, "POST /node/v1/cleanups", s.registerCleanup)
	s.handle(mux, "DELETE /node/v1/cleanups/{id}", s.releaseCleanup)
	s.http = &http.Server{
		Handler:           s.requireProtocol(mux),
		BaseContext:       func(net.Listener) context.Context { return s.ctx },
		MaxHeaderBytes:    8 << 10,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      resolveTimeout + 5*time.Second,
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

type nodeHandler func(w http.ResponseWriter, r *http.Request, g *grant)

func (s *Server) handle(mux *http.ServeMux, pattern string, h nodeHandler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		g := s.authenticate(r)
		if g == nil {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "a node bearer is required")
			return
		}
		h(w, r, g)
	})
}

// safety: a pipeline built against another protocol would misread every
// reply, so a request naming no version or another one gets 426.
func (s *Server) requireProtocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(authwire.NodeProtocolHeader)
		if got != authwire.NodeProtocolVersion {
			// safety: the header is attacker-sized and unauthenticated, so it is never echoed.
			s.writeJSON(w, http.StatusUpgradeRequired, map[string]any{
				"error":     "node protocol unsupported",
				"supported": []string{authwire.NodeProtocolVersion},
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.logger.Warn("node route reply not written", "status", status, "err", err)
	}
}
