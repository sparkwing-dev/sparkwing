package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// ListRunsHandler serves GET /api/v1/runs from the dashboard for
// topologies (e.g. S3-only) without a controller. Filter parsing routes
// through store.ParseRunFilter so dashboard and controller can't drift
// on query-param semantics.
func ListRunsHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := store.ParseRunFilter(r.URL.Query())
		runs, err := b.ListRuns(r.Context(), filter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if runs == nil {
			runs = []*store.Run{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	}
}

// GetRunHandler serves GET /api/v1/runs/{id}, mirroring the controller
// shape: bare run by default, {run, nodes} wrapper when
// ?include=nodes.
func GetRunHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		run, err := b.GetRun(r.Context(), runID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusNotFound, err)
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if includeHas(r.URL.Query().Get("include"), "nodes") {
			nodes, err := b.ListNodes(r.Context(), runID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			if nodes == nil {
				nodes = []*store.Node{}
			}
			// JSON null leaks to a runtime crash in the dashboard DAG
			// view (.length / .map on null).
			for _, n := range nodes {
				if n.Deps == nil {
					n.Deps = []string{}
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"run": run, "nodes": nodes})
			return
		}
		writeJSON(w, http.StatusOK, run)
	}
}

func includeHas(csv, target string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == target {
			return true
		}
	}
	return false
}
