package localws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// VersionInfo is the body of GET /api/v1/version: the running
// dashboard's own identity, used by `sparkwing dashboard start` to
// handshake a resident dashboard before deciding to replace it. The
// endpoint is unauthenticated by design -- it exposes no state, only
// the binary's own version and the schema it understands, which a
// starting CLI needs before it holds any credential.
type VersionInfo struct {
	Version string `json:"version"`
	Schema  int    `json:"schema"`
	PID     int    `json:"pid"`
}

// versionHandler serves the running dashboard's version handshake.
func versionHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(VersionInfo{
			Version: version,
			Schema:  store.ExpectedSchemaVersion(),
			PID:     os.Getpid(),
		})
	}
}

// schemaGuard watches for the shared state database being migrated to a
// schema this dashboard does not understand -- the failure mode where a
// newer binary upgrades the store out from under a resident reader,
// which would otherwise serve 500s until an operator notices. On the
// first observed skew it logs the concrete reason and cancels the
// server context so Run shuts down cleanly; the next `dashboard start`
// (or a supervisor) brings a matching binary back up.
type schemaGuard struct {
	st       *store.Store
	expected int
	cancel   context.CancelFunc
	once     sync.Once
}

func newSchemaGuard(st *store.Store, cancel context.CancelFunc) *schemaGuard {
	return &schemaGuard{st: st, expected: store.ExpectedSchemaVersion(), cancel: cancel}
}

// check reads the recorded schema version and, if the database has
// advanced past what this binary understands, triggers a single clean
// shutdown. A read failure is ignored: it is not evidence of skew, and
// the poller will try again.
func (g *schemaGuard) check(ctx context.Context) {
	if g == nil || g.st == nil {
		return
	}
	current, err := g.st.CurrentSchemaVersion(ctx)
	if err != nil {
		return
	}
	if current > g.expected {
		g.once.Do(func() {
			log.Printf(
				"dashboard: state database advanced to schema %d; this dashboard understands %d. "+
					"Shutting down cleanly -- restart with a matching sparkwing (sparkwing version update --cli).",
				current, g.expected)
			g.cancel()
		})
	}
}

// poll runs check on an interval until ctx is cancelled, as a safety
// net for the case where no request happens to trip the middleware.
func (g *schemaGuard) poll(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.check(ctx)
		}
	}
}

// middleware re-checks the schema whenever a wrapped handler returns a
// server error, so a schema skew that surfaces as a failing read
// triggers the clean exit at request time instead of after the next
// poll. Non-schema 5xxs are harmless: the schema is unchanged, so
// check is a no-op.
func (g *schemaGuard) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sc, r)
		if sc.status >= http.StatusInternalServerError {
			g.check(r.Context())
		}
	})
}

// statusRecorder captures the response status so the schema guard can
// react to server errors.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush and Unwrap keep streaming endpoints (log/event SSE) working
// through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
