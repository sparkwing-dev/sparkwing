package localws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
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

type schemaGuard struct {
	st     *store.Store
	cancel context.CancelFunc
	once   sync.Once
}

func newSchemaGuard(st *store.Store, cancel context.CancelFunc) *schemaGuard {
	return &schemaGuard{st: st, cancel: cancel}
}

// check shuts the dashboard down when the database gains a requirement this
// build cannot read. A schema number above this build's own is not enough: an
// additive migration leaves every requirement known, and this dashboard keeps
// serving the database a newer binary migrated.
func (g *schemaGuard) check(ctx context.Context) {
	if g == nil || g.st == nil {
		return
	}
	listed, err := g.st.Requirements(ctx)
	if err != nil {
		return
	}
	unknown := store.UnknownRequirements(listed)
	if len(unknown) == 0 {
		return
	}
	g.once.Do(func() {
		log.Printf(
			"dashboard: state database uses %s, which this dashboard does not understand. "+
				"Shutting down cleanly -- restart with a sparkwing that does (sparkwing update).",
			strings.Join(unknown, ", "))
		g.cancel()
	})
}

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

func (g *schemaGuard) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sc, r)
		if sc.status >= http.StatusInternalServerError {
			g.check(r.Context())
		}
	})
}

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
