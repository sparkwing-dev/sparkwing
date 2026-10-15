package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) reportingEndpoints() []Endpoint {
	engine := s.engineController
	if engine == nil {
		engine = &EngineController{Store: s.store}
	}
	endpoints := []Endpoint{
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/metrics", Decode: engine.runTarget, Handle: s.handleGetJobMetrics},
		{Method: "GET", Pattern: "/api/v1/runs", Decode: authorityTarget, Handle: s.handleListRuns},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/receipt", Decode: engine.runTarget, Handle: s.handleGetRunReceipt},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/attempts", Decode: engine.runTarget, Handle: s.handleListAttempts},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/steps", Decode: engine.runTarget, Handle: s.handleListNodeSteps},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/events", Decode: engine.runTarget, Handle: s.handleListEvents},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/debug-pauses", Decode: engine.runTarget, Handle: s.handleListDebugPauses},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/approvals", Decode: engine.runTarget, Handle: s.handleListApprovalsForRun},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/approvals/{job}", Decode: engine.runTarget, Handle: s.handleGetApproval},
		{Method: "GET", Pattern: "/api/v1/approvals", Decode: authorityTarget, Handle: s.handleListPendingApprovals},
		{Method: "GET", Pattern: "/api/v1/pipelines", Decode: authorityTarget, Handle: s.handleListPipelines},
		{Method: "GET", Pattern: "/api/v1/pipelines/{name}/latest", Decode: reportingPipelineTarget, Handle: s.handlePipelineLatest},
		{Method: "GET", Pattern: "/api/v1/sizing", Decode: authorityTarget, Handle: s.capacityRead(web.CapacityProfilesHandler)},
		{Method: "GET", Pattern: "/api/v1/sizing/explain", Decode: authorityTarget, Handle: s.capacityRead(web.CapacityExplainHandler)},
		{Method: "GET", Pattern: "/api/v1/trends", Decode: authorityTarget, Handle: s.handleTrends},
		{Method: "GET", Pattern: "/api/v1/runners", Decode: authorityTarget, Handle: s.handleAgents},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/logs", Decode: engine.runTarget, Handle: s.dashboardRead(web.RunLogsHandler, egress.ClassLog, egress.SlotDownload)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/logs/search", Decode: engine.runTarget, Handle: s.dashboardRead(func(b backend.Backend) http.HandlerFunc { return web.RunLogsSearchHandler(b, controllerSearchBounds) }, "", egress.SlotNone)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/logs", Decode: engine.runTarget, Handle: s.dashboardRead(web.NodeLogsHandler, egress.ClassLog, egress.SlotDownload)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/logs/stream", Decode: engine.runTarget, Handle: s.dashboardRead(web.NodeLogStreamHandler, egress.ClassLogStream, egress.SlotLogStream)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/logs/completeness", Decode: engine.runTarget, Handle: s.dashboardRead(web.NodeLogCompletenessHandler, "", egress.SlotNone)},
		{Method: "GET", Pattern: "/api/v1/runs/grep", Decode: authorityTarget, Handle: s.dashboardRead(func(b backend.Backend) http.HandlerFunc { return web.RunsGrepHandler(b, controllerSearchBounds) }, "", egress.SlotNone)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/events/stream", Decode: engine.runTarget, Handle: s.dashboardRead(web.EventsStreamHandler, "", egress.SlotNone)},
	}
	for i := range endpoints {
		endpoints[i].Authentication = capability.AuthCapability
	}
	return endpoints
}

func reportingPipelineTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{Pipeline: r.PathValue("name")}}, nil
}

func (s *Server) reportingTenant(r *http.Request, a capability.Authorized) (*store.Tenant, error) {
	if a.Target.Team == "" || s.store == nil {
		return nil, missingEndpointPolicy()
	}
	return s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
}
