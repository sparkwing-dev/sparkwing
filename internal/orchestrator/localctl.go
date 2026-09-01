package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const loopbackTokenTTL = 2 * time.Hour

const loopbackAuthCacheTTL = time.Minute

type loopbackController struct {
	url         string
	token       string
	tokenPrefix string
	srv         *http.Server
	store       *store.Store
	logger      *slog.Logger
}

func loopbackLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelWarn}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SPARKWING_LOG_FORMAT")), "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func startLoopbackController(
	st *store.Store,
	art storage.ArtifactStore,
	runID string,
	logger *slog.Logger,
) (*loopbackController, error) {
	if logger == nil {
		logger = loopbackLogger()
	}
	// safety: node processes write terminal state, events, and outputs, all of
	// which the controller gates behind admin. The token's blast radius
	// is bounded by the loopback bind and by revocation at run end
	// rather than by a narrower scope set.
	raw, tok, err := st.CreateToken(
		"local-run:"+runID, store.TokenKindService,
		[]string{controller.ScopeAdmin}, loopbackTokenTTL, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("loopback controller: mint run token: %w", err)
	}

	srvHandler := controller.New(st, logger).
		WithArtifactStore(art).
		WithAuthenticator(controller.NewAuthenticator(st, loopbackAuthCacheTTL)).
		Handler()

	url, srv, err := serveLoopback(srvHandler, runID, logger)
	if err != nil {
		_ = st.RevokeToken(tok.Prefix, time.Now().UTC())
		return nil, err
	}

	return &loopbackController{
		url:         url,
		token:       raw,
		tokenPrefix: tok.Prefix,
		srv:         srv,
		store:       st,
		logger:      logger,
	}, nil
}

func startLoopbackShim(
	state StateBackend,
	concurrency ConcurrencyBackend,
	art storage.ArtifactStore,
	runID string,
	logger *slog.Logger,
) (*loopbackController, error) {
	if logger == nil {
		logger = loopbackLogger()
	}
	token, err := newLoopbackToken()
	if err != nil {
		return nil, fmt.Errorf("loopback shim: mint run token: %w", err)
	}

	shim := controller.NewLoopback(state, runID, token, logger).
		WithConcurrency(concurrency).
		WithArtifactStore(art)

	url, srv, err := serveLoopback(shim.Handler(), runID, logger)
	if err != nil {
		return nil, err
	}
	return &loopbackController{url: url, token: token, srv: srv, logger: logger}, nil
}

func newLoopbackToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "swl_" + hex.EncodeToString(raw[:]), nil
}

func serveLoopback(h http.Handler, runID string, logger *slog.Logger) (string, *http.Server, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("loopback controller: listen: %w", err)
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := srv.Serve(lis); serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Warn("loopback controller stopped", "run_id", runID, "err", serveErr)
		}
	}()
	return "http://" + lis.Addr().String(), srv, nil
}

func (c *loopbackController) Close() {
	if c == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.srv.Shutdown(shutdownCtx); err != nil {
		c.logger.Debug("loopback controller shutdown", "err", err)
	}
	if c.store == nil {
		return
	}
	if err := c.store.RevokeToken(c.tokenPrefix, time.Now().UTC()); err != nil {
		c.logger.Warn("loopback controller: revoke run token", "err", err)
	}
}
