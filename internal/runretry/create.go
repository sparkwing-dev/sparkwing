package runretry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Created describes the durable retry rows and the source used to build them.
type Created struct {
	ID            string
	Source        *store.Run
	TriggerSource string
	StartedAt     time.Time
}

// Create persists a manual retry using the same trigger and pending-run shape
// for controller-backed and local queued execution.
func Create(ctx context.Context, st *store.Store, sourceID, newID string, full bool, now time.Time) (Created, error) {
	src, err := st.GetRun(ctx, sourceID)
	if err != nil {
		return Created{}, err
	}

	retrySource := "retry"
	if strings.HasPrefix(src.TriggerSource, "pipeline-working-tree@") {
		retrySource = src.TriggerSource
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID:            newID,
		Pipeline:      src.Pipeline,
		Args:          src.Args,
		TriggerSource: retrySource,
		TriggerEnv:    provenance(src),
		GitBranch:     src.GitBranch,
		GitSHA:        src.GitSHA,
		Repo:          src.Repo,
		RepoURL:       src.RepoURL,
		GithubOwner:   src.GithubOwner,
		GithubRepo:    src.GithubRepo,
		RetryOf:       sourceID,
		RetrySource:   store.RetrySourceManual,
		Full:          full,
		CreatedAt:     now,
	}); err != nil {
		return Created{}, fmt.Errorf("persist trigger: %w", err)
	}
	if err := st.CreateRun(ctx, store.Run{
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
		RetryOf:       sourceID,
		RetrySource:   store.RetrySourceManual,
		CreatedAt:     now,
		StartedAt:     now,
		Invocation:    store.InheritSecretArgs(nil, src),
	}); err != nil {
		return Created{}, fmt.Errorf("persist run: %w", err)
	}
	if err := st.SetRetriedAs(ctx, sourceID, newID); err != nil {
		slog.Default().Warn("could not link the source run to its retry", "run_id", sourceID, "retry_id", newID, "error", err)
	}

	return Created{ID: newID, Source: src, TriggerSource: retrySource, StartedAt: now}, nil
}

func provenance(src *store.Run) map[string]string {
	if src == nil || len(src.PlanSnapshot) == 0 {
		return nil
	}
	sum := sha256.Sum256(src.PlanSnapshot)
	planHash := "sha256:" + hex.EncodeToString(sum[:])
	if inherited := inheritedProvenance(src.Invocation["retry_provenance"]); inherited != nil {
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

func inheritedProvenance(raw any) map[string]string {
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
