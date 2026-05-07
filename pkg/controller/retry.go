package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// handleRetry creates a new run with the same pipeline + args as an
// existing run. The source run's status doesn't matter (retrying a
// running run is allowed). Trigger source is "retry" so callers can
// distinguish retries from originals.
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	srcID := r.PathValue("id")
	src, err := s.store.GetRun(r.Context(), srcID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	newID := newRunID()
	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:            newID,
		Pipeline:      src.Pipeline,
		Args:          src.Args,
		TriggerSource: "retry",
		TriggerUser:   "",
		TriggerEnv:    nil,
		GitBranch:     src.GitBranch,
		GitSHA:        src.GitSHA,
		Repo:          src.Repo,
		RepoURL:       src.RepoURL,
		GithubOwner:   src.GithubOwner,
		GithubRepo:    src.GithubRepo,
		RetryOf:       srcID,
		RetrySource:   store.RetrySourceManual,
		CreatedAt:     time.Now(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist trigger: %w", err))
		return
	}
	// Reverse pointer on the source row so older runs render a
	// "retried as #newID" pill. Best-effort.
	_ = s.store.SetRetriedAs(r.Context(), srcID, newID)

	if err := s.dispatcher.Dispatch(r.Context(), RunRequest{
		RunID:    newID,
		Pipeline: src.Pipeline,
		Args:     src.Args,
		Trigger:  sparkwing.TriggerInfo{Source: "retry"},
		Git: &sparkwing.Git{
			Branch:  src.GitBranch,
			SHA:     src.GitSHA,
			Repo:    src.Repo,
			RepoURL: src.RepoURL,
		},
		RetryOf: srcID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Return a Run-shaped body the dashboard's api.ts consumes
	// directly, so the caller doesn't need a round-trip to GetRun.
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":          newID,
		"pipeline":    src.Pipeline,
		"status":      "running",
		"trigger":     "retry",
		"git_branch":  src.GitBranch,
		"git_sha":     src.GitSHA,
		"started_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"duration_ms": 0,
		"retry_of":    srcID,
	})
}
