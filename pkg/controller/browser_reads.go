package controller

import (
	"context"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) dashboardRead(read func(backend.Backend) http.HandlerFunc, class egress.Class, slot egress.Slot) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		tenant, err := s.reportingTenant(r, a)
		if err != nil {
			return engineLookupError(err)
		}
		b := backend.NewStoreBackend(s.store, s.dashboardPaths(), s.dashboardLogs(tenant, store.Actor{Authority: a.Principal.Authority, CapabilityPrefix: a.Principal.Capability.Prefix})).Scoped(tenant, liveLogReader{s.liveLogs})
		r.SetPathValue("id", a.Target.RunID)
		r.SetPathValue("node", a.Target.NodeID)
		handler := read(b)
		if class == "" {
			handler.ServeHTTP(w, r)
		} else {
			s.meterResponse(w, r, class, slot, handler, egress.TeamPrincipal(a.Target.Team, a.Principal.Capability.Subject.ID), tenant.Team())
		}
		return nil
	}
}

func (s *Server) capacityRead(read func(web.ProfileReader) http.HandlerFunc) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		var reader web.ProfileReader
		if s.dashboard != nil && s.dashboard.Local {
			tenant, err := s.reportingTenant(r, a)
			if err != nil {
				return engineLookupError(err)
			}
			reader = tenant
		}
		read(reader).ServeHTTP(w, r)
		return nil
	}
}

func (s *Server) dashboardPaths() paths.Paths {
	if s.dashboard != nil {
		return s.dashboard.Paths
	}
	return paths.Paths{}
}

// safety: a laptop dashboard reads the runs on its own disk, so a nil store there sends reads to the run
// directories; elsewhere nil means a controller with nowhere to keep segments, so a read finds no log.
func (s *Server) dashboardLogs(tenant *store.Tenant, actor store.Actor) storage.LogStore {
	if s.dashboard != nil && s.dashboard.Local {
		return nil
	}
	objs := s.logObjects()
	if objs == nil {
		return nil
	}
	return segmentLogs{records: tenant, actor: actor, live: s.liveLogs, objs: objs}
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
