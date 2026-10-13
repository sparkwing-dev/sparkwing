package controller

import (
	"context"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the run-scoped reads sit behind the team boundary, which answers another team's run as missing; the
// run list behind them is the request's own team's, so a grep never reaches another team's logs.
func (s *Server) dashboardRead(read func(backend.Backend) http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := s.requestTenant(w, r)
		if !ok {
			return
		}
		b := backend.NewStoreBackend(s.store, s.dashboardPaths(), s.dashboardLogs(tenant.Team())).
			Scoped(tenant, liveLogReader{s.liveLogs})
		read(b).ServeHTTP(w, r)
	})
}

// safety: learned capacity profiles are machine-wide rows with no team, so only a host's own dashboard reads them.
func (s *Server) capacityRead(read func(web.ProfileReader) http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reader web.ProfileReader
		if s.dashboard != nil && s.dashboard.Local {
			reader = s.store
		}
		read(reader).ServeHTTP(w, r)
	})
}

func (s *Server) dashboardPaths() paths.Paths {
	if s.dashboard != nil {
		return s.dashboard.Paths
	}
	return paths.Paths{}
}

// safety: a laptop dashboard reads the runs on its own disk, so a nil store there sends reads to the run
// directories; elsewhere nil means a controller with nowhere to keep segments, so a read finds no log.
func (s *Server) dashboardLogs(team store.Team) storage.LogStore {
	if s.dashboard != nil && s.dashboard.Local {
		return nil
	}
	objs := s.logObjects()
	if objs == nil {
		return nil
	}
	return segmentLogs{s: s, team: team, objs: objs}
}

type liveLogReader struct{ l *liveLogs }

func (l liveLogReader) StreamNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (io.ReadCloser, error) {
	if _, ok := l.l.Read(runID, nodeID, since); !ok {
		return nil, nil
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(streamLiveLog(ctx, l.l, pipeSink{pw}, runID, nodeID, since))
	}()
	return pr, nil
}

func (l liveLogReader) ReadNodeLiveLog(_ context.Context, runID, nodeID string, since int64) ([]byte, int64, bool, bool, error) {
	chunk, ok := l.l.Read(runID, nodeID, since)
	if !ok {
		return nil, 0, false, false, nil
	}
	return chunk.Data, chunk.Next, chunk.Done, true, nil
}

type pipeSink struct{ *io.PipeWriter }

func (pipeSink) Flush() error { return nil }
