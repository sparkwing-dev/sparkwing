package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// maxPipelinesListed bounds one answer, since a team's pipeline count grows
// with every name it ever ran.
const maxPipelinesListed = 200

// handleListPipelines lists the caller's team's pipelines, most recently
// active first, each with its latest run.
func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	limit := maxPipelinesListed
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = min(n, maxPipelinesListed)
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	pipelines, err := tenant.ListPipelines(r.Context(), limit)
	if err != nil {
		s.writeInternalError(w, r, "list pipelines", err)
		return
	}
	writeJSON(w, http.StatusOK, pipelinesList{Pipelines: pipelines})
}

type pipelinesList struct {
	Pipelines []store.PipelineSummary `json:"pipelines"`
}
