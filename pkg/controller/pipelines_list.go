package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const maxPipelinesListed = 200

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
