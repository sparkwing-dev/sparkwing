package controller

import (
	"net/http"

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
		b := backend.NewStoreBackend(tenant, s.dashboardLogs(tenant, engineActor(a)))
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
