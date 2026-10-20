package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/web"
)

func (s *Server) reportingEndpoints() []Endpoint {
	engine := s.engineController
	endpoints := []Endpoint{
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/metrics", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleGetJobMetrics},
		{Method: "GET", Pattern: "/api/v1/runs", Decode: authorityTarget, Handle: s.handleListRuns},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/receipt", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleGetRunReceipt},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/attempts", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleListAttempts},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/steps", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleListNodeSteps},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/events", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleListEvents},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/debug-pauses", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleListDebugPauses},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/approvals", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleListApprovalsForRun},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/approvals/{job}", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleGetApproval},
		{Method: "GET", Pattern: "/api/v1/approvals", Decode: authorityTarget, Handle: s.handleListPendingApprovals},
		{Method: "GET", Pattern: "/api/v1/pipelines", Decode: authorityTarget, Handle: s.handleListPipelines},
		{Method: "GET", Pattern: "/api/v1/pipelines/{name}/latest", Decode: reportingPipelineTarget, Handle: s.handlePipelineLatest},
		{Method: "GET", Pattern: "/api/v1/sizing", Decode: authorityTarget, Handle: s.capacityRead(web.CapacityProfilesHandler)},
		{Method: "GET", Pattern: "/api/v1/sizing/explain", Decode: authorityTarget, Handle: s.capacityRead(web.CapacityExplainHandler)},
		{Method: "GET", Pattern: "/api/v1/trends", Decode: authorityTarget, Handle: s.handleTrends},
		{Method: "GET", Pattern: "/api/v1/runners", Decode: authorityTarget, Handle: s.handleAgents},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/logs", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.dashboardRead(web.RunLogsHandler, egress.ClassLog, egress.SlotDownload)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/logs/search", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.logScanSlot(s.dashboardRead(func(b backend.Backend) http.HandlerFunc { return web.RunLogsSearchHandler(b, controllerSearchBounds) }, "", egress.SlotNone))},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/logs", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.dashboardRead(web.NodeLogsHandler, egress.ClassLog, egress.SlotDownload)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/logs/stream", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.dashboardRead(web.NodeLogStreamHandler, egress.ClassLogStream, egress.SlotLogStream)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/logs/completeness", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.dashboardRead(web.NodeLogCompletenessHandler, "", egress.SlotNone)},
		{Method: "GET", Pattern: "/api/v1/runs/grep", Decode: authorityTarget, Handle: s.logScanSlot(s.dashboardRead(func(b backend.Backend) http.HandlerFunc { return web.RunsGrepHandler(b, controllerSearchBounds) }, "", egress.SlotNone))},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/events/stream", Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.dashboardRead(web.EventsStreamHandler, "", egress.SlotNone)},
	}
	for i := range endpoints {
		endpoints[i].Authentication = capability.AuthCapability
		switch endpoints[i].Pattern {
		case "/api/v1/runs/grep":
			endpoints[i].Handle = s.pagedLogSearch(endpoints[i].Handle)
		case "/api/v1/runs/{run}/logs", "/api/v1/runs/{run}/logs/search", "/api/v1/runs/{run}/jobs/{job}/logs":
			endpoints[i].Handle = s.pagedLogRead(endpoints[i].Handle)
		}
	}
	return endpoints
}

func (s *Server) logScanSlot(next func(http.ResponseWriter, *http.Request, capability.Authorized) error) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		release, ok := s.holdLogScanSlot(w, r, egress.TeamPrincipal(a.Target.Team, a.Principal.Capability.Subject.ID))
		if !ok {
			return nil
		}
		defer release()
		return next(w, r, a)
	}
}

func reportingPipelineTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{Pipeline: r.PathValue("name")}}, nil
}

func (s *Server) reportingTenant(r *http.Request, a capability.Authorized) (*store.Tenant, error) {
	if a.Target.Team == "" || s.operator == nil {
		return nil, missingEndpointPolicy()
	}
	return s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
}
