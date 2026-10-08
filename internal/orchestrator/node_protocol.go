package orchestrator

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

// hack: pipeline binaries pin the SDK they were built with, so a request naming another
// node protocol version, or none, is logged and served rather than refused.
type nodeProtocolCheck struct {
	logger *slog.Logger
	logged atomic.Bool
}

func (c *nodeProtocolCheck) observe(r *http.Request) {
	got := r.Header.Get(authwire.NodeProtocolHeader)
	// perf: one line per listener, because every request of an old binary misses.
	if got == authwire.NodeProtocolVersion || c.logger == nil || c.logged.Swap(true) {
		return
	}
	c.logger.Warn("node protocol version mismatch; serving the request",
		"want", authwire.NodeProtocolVersion, "got", got, "method", r.Method, "path", r.URL.Path)
}

func (c *nodeProtocolCheck) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.observe(r)
		h.ServeHTTP(w, r)
	})
}
