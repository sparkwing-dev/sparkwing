package orchestrator

import (
	"context"
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

// loopbackTokenTTL bounds the run-scoped bearer for the case where
// teardown never runs -- a dispatcher killed with SIGKILL revokes
// nothing. Until it expires, a full-admin bearer for the machine's
// run store is sitting in a dead process's environment, so the window
// is hours rather than a day. Two hours covers a long local run with
// room to spare.
//
// Expiry is the backstop, not the mechanism: a clean teardown revokes
// immediately. Even then the authenticator's cache keeps a revoked
// token working for up to its TTL (see the cache argument below), so
// revocation is prompt rather than instant.
const loopbackTokenTTL = 2 * time.Hour

// loopbackAuthCacheTTL trades revocation latency against an argon2
// verify per request. A node writes state on every step boundary, so
// verifying each one uncached is real cost; a minute of staleness on
// a loopback-only token is not.
const loopbackAuthCacheTTL = time.Minute

// loopbackController is the controller a local run mounts for its own
// node processes.
//
// The node processes could not simply open the run's SQLite file:
// several writers on one SQLite database is the contention this
// repository already carries a wedge-guard subsystem for, because it
// was hit. Mounting the same controller a pod talks to keeps one
// writer -- the dispatcher's process -- and gives local and cluster
// execution the same state API.
//
// It binds 127.0.0.1 on an ephemeral port and is never announced in
// dev.env: the address belongs to this run, not to the machine, and a
// resident dashboard's dev.env entry is what other tools read.
type loopbackController struct {
	url         string
	token       string
	tokenPrefix string
	srv         *http.Server
	store       *store.Store
	logger      *slog.Logger
}

// loopbackLogger is what the loopback controller says when the caller
// names no logger.
//
// The level is Warn, not the default: this controller serves a request
// per state write of every node, and at info level that is a request
// log per line the operator never asked for, printed over the run
// output they did.
//
// The format follows the run's own. A warning rendered as logfmt in
// the middle of a JSON log stream is a line whatever is consuming that
// stream cannot parse, which is how a warning becomes an error
// somewhere downstream.
func loopbackLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelWarn}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SPARKWING_LOG_FORMAT")), "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// startLoopbackController mounts the controller and mints the run's
// bearer. The caller owns the returned handle and must Close it.
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

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = st.RevokeToken(tok.Prefix, time.Now().UTC())
		return nil, fmt.Errorf("loopback controller: listen: %w", err)
	}

	srv := &http.Server{
		Handler:           srvHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := srv.Serve(lis); serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Warn("loopback controller stopped", "run_id", runID, "err", serveErr)
		}
	}()

	return &loopbackController{
		url:         "http://" + lis.Addr().String(),
		token:       raw,
		tokenPrefix: tok.Prefix,
		srv:         srv,
		store:       st,
		logger:      logger,
	}, nil
}

// Close stops serving and revokes the run's bearer. Both halves run
// even if the first fails: a token that outlives its run is the part
// that matters.
func (c *loopbackController) Close() {
	if c == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.srv.Shutdown(shutdownCtx); err != nil {
		c.logger.Debug("loopback controller shutdown", "err", err)
	}
	if err := c.store.RevokeToken(c.tokenPrefix, time.Now().UTC()); err != nil {
		c.logger.Warn("loopback controller: revoke run token", "err", err)
	}
}
