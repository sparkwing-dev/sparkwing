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
	retrySource := "retry"
	if strings.HasPrefix(src.TriggerSource, "pipeline-working-tree@") {
		retrySource = src.TriggerSource
	}

	newID := newRunID()
	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:            newID,
		Pipeline:      src.Pipeline,
		Args:          src.Args,
		TriggerSource: retrySource,
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
		TriggerSource: retrySource,
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

		Invocation: store.InheritSecretArgs(nil, src),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist run: %w", err))
		return
	}
	_ = s.store.SetRetriedAs(r.Context(), srcID, newID)

	if err := s.dispatcher.Dispatch(r.Context(), RunRequest{
		RunID:    newID,
		Pipeline: src.Pipeline,
		Args:     src.Args,
		Trigger:  sparkwing.TriggerInfo{Source: retrySource},
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
		"trigger_source": retrySource,
		"git_branch":     src.GitBranch,
		"git_sha":        src.GitSHA,
		"started_at":     now.UTC().Format(time.RFC3339Nano),
		"duration_ms":    0,
		"retry_of":       srcID,
	})
}

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
