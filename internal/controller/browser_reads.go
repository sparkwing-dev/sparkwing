package controller

import (
	"context"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/web"
)

func (s *Server) dashboardRead(read func(backend.Backend) http.HandlerFunc, class egress.Class, slot egress.Slot) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		tenant, err := s.reportingTenant(r, a)
		if err != nil {
			return engineLookupError(err)
		}
		var b backend.Backend = backend.NewStoreBackend(tenant, s.dashboardLogs(tenant, engineActor(a)))
		if a.Principal.Authority.Kind == capability.AuthorityCapability || a.Principal.Authority.Kind == capability.AuthoritySession {
			pattern := strings.TrimPrefix(r.Pattern, r.Method+" ")
			b = &recheckedEvents{Backend: b, recheck: func(ctx context.Context) error { return tenant.Reauthorize(ctx, a, r.Method, pattern) }}
		}
		r.SetPathValue("run", a.Target.RunID)
		r.SetPathValue("job", a.Target.NodeID)
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

func (s *Server) dashboardLogs(tenant *store.Tenant, actor runstate.Actor) storage.LogStore {
	return segmentLogs{records: tenant, actor: actor, live: s.liveLogs, objs: s.logObjects()}
}

// recheckedEvents ends an event stream whose reader lost its authority: log
// tails reauthorize every read, and event polls rerun the route's own
// authorization every few seconds, so a claim reader also stops when its run
// is cancelled or its claim moves on.
type recheckedEvents struct {
	backend.Backend
	recheck func(context.Context) error
	polls   int
}

const eventStreamRecheckEvery = 8

func (b *recheckedEvents) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	b.polls++
	if b.polls%eventStreamRecheckEvery == 0 {
		if err := b.recheck(ctx); err != nil {
			return nil, err
		}
	}
	return b.Backend.ListEventsAfter(ctx, runID, afterSeq, limit)
}
