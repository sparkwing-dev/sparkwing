package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	runs = store.RedactedRuns(runs)
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
	retryEnv := retryProvenance(src)

	newID := newRunID()
	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:            newID,
		Pipeline:      src.Pipeline,
		Args:          src.Args,
		TriggerSource: "retry",
		TriggerUser:   "",
		TriggerEnv:    retryEnv,
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

// retryProvenance binds a local retry to the exact checkout and static plan
// recorded by its source attempt. Cluster runs may not have a host-visible cwd;
// leaving the keys absent preserves their remote dispatch path, while the local
// consumer treats missing keys on a retry as an unavailable source worktree.
func retryProvenance(src *store.Run) map[string]string {
	if src == nil {
		return nil
	}
	if len(src.PlanSnapshot) == 0 {
		return nil
	}
	sum := sha256.Sum256(src.PlanSnapshot)
	planHash := "sha256:" + hex.EncodeToString(sum[:])
	if inherited := inheritedRetryProvenance(src.Invocation["retry_provenance"]); inherited != nil {
		inherited[retryprovenance.PlanHashKey] = planHash
		return inherited
	}

	cwd, _ := src.Invocation["cwd"].(string)
	if cwd == "" {
		return nil
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	cwd = filepath.Clean(cwd)
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	return map[string]string{
		retryprovenance.RepoDirKey:      cwd,
		retryprovenance.RepoIdentityKey: src.RepoURL,
		retryprovenance.RevisionKey:     src.GitSHA,
		retryprovenance.PlanHashKey:     planHash,
	}
}

// inheritedRetryProvenance keeps a retry chain bound to its original durable
// checkout. A retry run's cwd is an intentionally short-lived snapshot, so a
// later retry must inherit the recorded source rather than capture that temp
// directory. Invocation data may be freshly built or JSON-decoded from storage.
func inheritedRetryProvenance(raw any) map[string]string {
	var value func(string) string
	switch provenance := raw.(type) {
	case map[string]string:
		value = func(key string) string { return provenance[key] }
	case map[string]any:
		value = func(key string) string {
			v, _ := provenance[key].(string)
			return v
		}
	default:
		return nil
	}
	repoDir := strings.TrimSpace(value("repo_dir"))
	repoIdentity := strings.TrimSpace(value("repo_identity"))
	revision := strings.TrimSpace(value("revision"))
	if repoDir == "" || repoIdentity == "" || revision == "" {
		return nil
	}
	return map[string]string{
		retryprovenance.RepoDirKey:      repoDir,
		retryprovenance.RepoIdentityKey: repoIdentity,
		retryprovenance.RevisionKey:     revision,
	}
}
