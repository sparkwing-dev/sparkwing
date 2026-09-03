package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func ListRunsHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, parseErr := store.ParseRunFilterValidated(r.URL.Query())
		if parseErr != nil {
			writeErr(w, http.StatusBadRequest, parseErr)
			return
		}
		runs, err := b.ListRuns(r.Context(), filter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		runs = store.RedactedRuns(runs)
		if runs == nil {
			runs = []*store.Run{}
		}
		w.Header().Set("X-Sparkwing-Run-Filter-Version", "1")
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	}
}

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
			nodes = api.PublicNodes(nodes)
			// safety: JSON null for Deps crashes the dashboard DAG view (.length / .map on null).
			for _, n := range nodes {
				if n.Deps == nil {
					n.Deps = []string{}
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"run": store.RedactedRun(run), "nodes": nodes})
			return
		}
		writeJSON(w, http.StatusOK, store.RedactedRun(run))
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
