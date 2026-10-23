package controller

import (
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

const maxPipelinesListed = 200

func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	limit := maxPipelinesListed
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return invalidEngineBody("limit must be a positive integer")
		}
		limit = min(n, maxPipelinesListed)
	}
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	pipelines, err := tenant.ListPipelines(r.Context(), limit)
	if err != nil {
		return engineLookupError(err)
	}
	var out []controllerwire.PipelineSummary
	if pipelines != nil {
		out = make([]controllerwire.PipelineSummary, len(pipelines))
	}
	for i, row := range pipelines {
		out[i] = controllerwire.PipelineSummary(row)
	}
	writeJSON(w, http.StatusOK, pipelinesList{Pipelines: out})
	return nil
}

type pipelinesList struct {
	Pipelines []controllerwire.PipelineSummary `json:"pipelines"`
}
