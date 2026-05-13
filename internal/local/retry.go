package local

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// handleListAttempts returns every run in the same retry tree as
// the requested id, ordered oldest-first. The dashboard's Attempts
// dropdown numbers them sequentially -- branches (e.g. attempt #2
// retried twice) appear as siblings ordered by created_at, so
// chronological numbering stays linear even when the underlying
// retry_of graph has forks.
//
// Response shape mirrors GET /api/v1/runs: { "runs": [Run, ...] }
// so the existing client decoder works unchanged.
func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runs, err := s.store.ListRunRetryTree(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if runs == nil {
		runs = []*store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleRetry creates a new run with the same pipeline + args as an
// existing run. The source run's status doesn't matter (retrying a
// running run is allowed). Trigger source is "retry" so callers can
// distinguish retries from originals.
//
// Query parameters:
//   - full=1  re-execute every node, ignoring the skip-passed
//     rehydration that retry_of would normally trigger. This is the
//     "Rerun all" choice on the dashboard retry menu. Default "Rerun
//     from failed" leaves full unset (skip-passed kicks in).
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

	full := r.URL.Query().Get("full") == "1"

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
		Full:          full,
		CreatedAt:     time.Now(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist trigger: %w", err))
		return
	}
	// Pre-allocate the Run row at retry-intake -- mirror handleTrigger
	// so the dashboard's runs list shows the new attempt instantly,
	// instead of waiting the ~500ms+compile delay until the consumer
	// claims the trigger and the orchestrator emits its own CreateRun.
	// Status starts as "pending"; the orchestrator's CreateRun upsert
	// promotes it to "running" once the subprocess actually starts.
	now := time.Now()
	if err := s.store.CreateRun(r.Context(), store.Run{
		ID:            newID,
		Pipeline:      src.Pipeline,
		Status:        "pending",
		TriggerSource: "retry",
		GitBranch:     src.GitBranch,
		GitSHA:        src.GitSHA,
		Args:          src.Args,
		Repo:          src.Repo,
		RepoURL:       src.RepoURL,
		GithubOwner:   src.GithubOwner,
		GithubRepo:    src.GithubRepo,
		RetryOf:       srcID,
		RetrySource:   store.RetrySourceManual,
		CreatedAt:     now,
		StartedAt:     now,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist run: %w", err))
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
		"id":             newID,
		"pipeline":       src.Pipeline,
		"status":         "pending",
		"trigger_source": "retry",
		"git_branch":     src.GitBranch,
		"git_sha":        src.GitSHA,
		"started_at":     now.UTC().Format(time.RFC3339Nano),
		"duration_ms":    0,
		"retry_of":       srcID,
	})
}
