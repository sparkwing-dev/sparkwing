package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runretry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runs, err := s.store.ListRunRetryTree(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runs = store.RedactedRuns(runs)
	if runs == nil {
		runs = []*store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	srcID := r.PathValue("id")
	full := r.URL.Query().Get("full") == "1"
	created, err := runretry.Create(r.Context(), s.store, srcID, newRunID(), full, time.Now())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.dispatcher.Dispatch(r.Context(), RunRequest{
		RunID:    created.ID,
		Pipeline: created.Source.Pipeline,
		Args:     created.Source.Args,
		Trigger:  sparkwing.TriggerInfo{Source: created.TriggerSource},
		Git: &sparkwing.Git{
			Branch:  created.Source.GitBranch,
			SHA:     created.Source.GitSHA,
			Repo:    created.Source.Repo,
			RepoURL: created.Source.RepoURL,
		},
		RetryOf: srcID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":             created.ID,
		"pipeline":       created.Source.Pipeline,
		"status":         "pending",
		"trigger_source": created.TriggerSource,
		"git_branch":     created.Source.GitBranch,
		"git_sha":        created.Source.GitSHA,
		"started_at":     created.StartedAt.UTC().Format(time.RFC3339Nano),
		"duration_ms":    0,
		"retry_of":       srcID,
	})
}
