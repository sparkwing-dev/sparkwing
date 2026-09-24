package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/runretry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runs, err := tenant.ListRunRetryTree(r.Context(), id)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	source, err := tenant.GetRun(r.Context(), srcID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "read retry source", err)
		return
	}
	if strings.HasPrefix(source.TriggerSource, "pipeline-working-tree@") {
		trigger, triggerErr := s.store.GetTrigger(r.Context(), srcID)
		if triggerErr != nil && !errors.Is(triggerErr, store.ErrNotFound) {
			s.writeInternalError(w, r, "read retry source trigger", triggerErr)
			return
		}
		// A local working-tree run can have no trigger row; its source remains retryable.
		if triggerErr == nil && trigger.Team == tenant.Team() && trigger.TriggerEnv[bincache.SourceBundleObjectEnvKey] != "" {
			writeError(w, http.StatusUnprocessableEntity, errors.New("cloud working-tree retry needs a new source upload; rerun sparkwing run <pipeline> --profile <cloud-profile> from the checkout"))
			return
		}
	}
	if !s.admitTriggerSubmission(w, r, s.floodKey(r, "retry:"+srcID), "retry") {
		return
	}
	// safety: a retry creates a run, so the hourly guard measures the principal
	// that asked for it exactly as a direct create does.
	retryCtx := store.WithCreatingPrincipal(r.Context(), claimIdentity(r).Principal)
	created, err := runretry.Create(retryCtx, s.store, srcID, newRunID(), full, time.Now())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if s.writeComputeLimitRefusal(w, r, "", "", err) {
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
			Repo:    created.Source.DeclaredRepo,
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
