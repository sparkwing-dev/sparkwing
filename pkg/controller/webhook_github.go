package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const webhookBodyLimit = 1 << 20

// WithGitHubWebhookSecret installs the shared secret used to verify
// incoming GitHub webhook signatures. It is the fallback for
// deliveries that no narrower secret in [GitHubWebhookConfig] covers;
// when neither resolves, /webhooks/github returns 503. Every GitHub
// webhook relying on this fallback must be configured with this secret.
func (s *Server) WithGitHubWebhookSecret(secret string) *Server {
	s.githubWebhookSecret = secret
	return s
}

// GitHubWebhookBinding declares which repositories may drive one
// pipeline through POST /webhooks/github/{pipeline}, and optionally the
// secret that pipeline's deliveries are signed with.
//
// A non-empty Repos list is an allow-list of "owner/name" slugs matched
// case-insensitively: a delivery naming any other repository is
// refused with 403, so holding a secret no longer means holding every
// pipeline. An empty list binds no repository and leaves the delivery's
// repository unchecked, which is the behavior of an unconfigured
// pipeline.
type GitHubWebhookBinding struct {
	Repos  []string `json:"repos,omitempty"`
	Secret string   `json:"secret,omitempty"`
}

// GitHubWebhookConfig narrows GitHub webhook intake below the single
// shared secret installed by [Server.WithGitHubWebhookSecret].
// Pipelines is keyed by the {pipeline} path segment; RepoSecrets is
// keyed by repository slug ("owner/name", matched case-insensitively)
// so each repository owner can be handed a secret of their own.
//
// The handler resolves the signing secret most specific first: the
// pipeline's own secret, then the secret of the repository the delivery
// names, then the shared secret. Give every bound repository its own
// secret to isolate them completely; a repository left without one is
// verified with the shared secret, which its peers also hold.
type GitHubWebhookConfig struct {
	Pipelines   map[string]GitHubWebhookBinding `json:"pipelines,omitempty"`
	RepoSecrets map[string]string               `json:"repo_secrets,omitempty"`
}

// WithGitHubWebhookConfig installs cfg as the repository binding and
// per-pipeline / per-repository secret table for GitHub webhook intake.
// It composes with [Server.WithGitHubWebhookSecret], which stays the
// fallback for anything cfg does not name.
func (s *Server) WithGitHubWebhookConfig(cfg GitHubWebhookConfig) *Server {
	s.githubWebhook = GitHubWebhookConfig{Pipelines: cfg.Pipelines}
	if len(cfg.RepoSecrets) > 0 {
		s.githubWebhook.RepoSecrets = make(map[string]string, len(cfg.RepoSecrets))
		for repo, secret := range cfg.RepoSecrets {
			s.githubWebhook.RepoSecrets[strings.ToLower(repo)] = secret
		}
	}
	return s
}

func (s *Server) githubWebhookSecretFor(pipeline, repo string) string {
	if b, ok := s.githubWebhook.Pipelines[pipeline]; ok && b.Secret != "" {
		return b.Secret
	}
	if repo != "" {
		if secret := s.githubWebhook.RepoSecrets[strings.ToLower(repo)]; secret != "" {
			return secret
		}
	}
	return s.githubWebhookSecret
}

func (s *Server) githubWebhookRepoAllowed(pipeline, repo string) bool {
	b, ok := s.githubWebhook.Pipelines[pipeline]
	if !ok || len(b.Repos) == 0 {
		return true
	}
	if repo == "" {
		return false
	}
	for _, bound := range b.Repos {
		if strings.EqualFold(bound, repo) {
			return true
		}
	}
	return false
}

func githubPayloadRepo(body []byte) string {
	var envelope struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return envelope.Repository.FullName
}

type githubPushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Before     string `json:"before"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"pusher"`
	HeadCommit *struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	pipeline := r.PathValue("pipeline")
	if pipeline == "" {
		writeError(w, http.StatusBadRequest, errors.New("pipeline path segment required"))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookBodyLimit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}

	// safety: the repository comes from the unverified body only to choose which secret must have signed it.
	claimedRepo := githubPayloadRepo(body)
	secret := s.githubWebhookSecretFor(pipeline, claimedRepo)
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable,
			errors.New("github webhook secret not configured on controller"))
		return
	}

	if !verifyGitHubSignature(r.Header.Get("X-Hub-Signature-256"), body, secret) {
		writeError(w, http.StatusUnauthorized, errors.New("signature mismatch"))
		return
	}

	if !s.githubWebhookRepoAllowed(pipeline, claimedRepo) {
		s.logger.Warn("github webhook rejected",
			"pipeline", pipeline, "repo", claimedRepo, "reason", "repository not bound to pipeline")
		writeError(w, http.StatusForbidden,
			errors.New("repository is not bound to this pipeline"))
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")
	// safety: without a delivery id the replay constraint has nothing to key on, so an unsigned replay would pass.
	if delivery == "" {
		writeError(w, http.StatusBadRequest, errors.New("X-GitHub-Delivery header required"))
		return
	}

	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	case "push":
		s.handleGitHubPush(w, r, pipeline, delivery, body)
		return
	case "pull_request":
		s.handleGitHubPullRequest(w, r, pipeline, delivery, body)
		return
	default:
		s.logger.Info("github webhook ignored",
			"event", event, "pipeline", pipeline, "delivery", delivery)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "ignored",
			"event":  event,
		})
	}
}

func (s *Server) handleGitHubPush(w http.ResponseWriter, r *http.Request, pipeline, delivery string, body []byte) {
	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode push payload: %w", err))
		return
	}

	if payload.Deleted {
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "ignored",
			"reason": "branch deleted",
		})
		return
	}
	branch, ok := strings.CutPrefix(payload.Ref, "refs/heads/")
	if !ok {
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "ignored",
			"reason": "non-branch ref",
			"ref":    payload.Ref,
		})
		return
	}

	runID := newRunID()
	trigger := sparkwing.TriggerInfo{
		Source: "github",
		User:   payload.Pusher.Name,
	}
	triggerEnv := map[string]string{
		"GITHUB_DELIVERY":   delivery,
		"GITHUB_REPOSITORY": payload.Repository.FullName,
		"GITHUB_BEFORE":     payload.Before,
		"GITHUB_AFTER":      payload.After,
	}
	owner, repoName := "", ""
	if parts := strings.SplitN(payload.Repository.FullName, "/", 2); len(parts) == 2 {
		owner, repoName = parts[0], parts[1]
	}
	g := &sparkwing.Git{
		Branch: branch,
		SHA:    payload.After,
		Repo:   payload.Repository.FullName,
	}

	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:              runID,
		Pipeline:        pipeline,
		TriggerSource:   trigger.Source,
		TriggerUser:     trigger.User,
		TriggerEnv:      triggerEnv,
		GitBranch:       g.Branch,
		GitSHA:          g.SHA,
		Repo:            g.Repo,
		GithubOwner:     owner,
		GithubRepo:      repoName,
		WebhookDelivery: delivery,
		CreatedAt:       time.Now(),
	}); err != nil {
		if errors.Is(err, store.ErrDuplicateWebhookDelivery) {
			writeError(w, http.StatusConflict, errors.New("delivery already accepted"))
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist trigger: %w", err))
		return
	}

	if err := s.dispatcher.Dispatch(r.Context(), RunRequest{
		RunID:    runID,
		Pipeline: pipeline,
		Trigger:  trigger,
		Git:      g,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.logger.Info(
		"github webhook accepted",
		"pipeline", pipeline,
		"run_id", runID,
		"repo", payload.Repository.FullName,
		"branch", branch,
		"sha", payload.After,
		"delivery", delivery,
	)
	writeJSON(w, http.StatusAccepted, triggerResp{
		RunID:  runID,
		Status: "dispatched",
	})
}

type githubPullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

var defaultPullRequestActions = map[string]struct{}{
	"opened":      {},
	"synchronize": {},
	"reopened":    {},
}

func (s *Server) handleGitHubPullRequest(w http.ResponseWriter, r *http.Request, pipeline, delivery string, body []byte) {
	var payload githubPullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode pull_request payload: %w", err))
		return
	}

	if _, ok := defaultPullRequestActions[payload.Action]; !ok {
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "ignored",
			"reason": "pull_request action not built",
			"action": payload.Action,
		})
		return
	}

	runID := newRunID()
	trigger := sparkwing.TriggerInfo{
		Source: "github",
		User:   payload.PullRequest.User.Login,
	}
	triggerEnv := map[string]string{
		"GITHUB_DELIVERY":            delivery,
		"GITHUB_REPOSITORY":          payload.Repository.FullName,
		sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest,
		sparkwing.EnvPRNumber:        strconv.Itoa(payload.Number),
		sparkwing.EnvPRAction:        payload.Action,
		sparkwing.EnvPRBaseRef:       payload.PullRequest.Base.Ref,
		sparkwing.EnvPRBaseSHA:       payload.PullRequest.Base.SHA,
		sparkwing.EnvPRHeadRef:       payload.PullRequest.Head.Ref,
		sparkwing.EnvPRHeadSHA:       payload.PullRequest.Head.SHA,
	}
	owner, repoName := "", ""
	if parts := strings.SplitN(payload.Repository.FullName, "/", 2); len(parts) == 2 {
		owner, repoName = parts[0], parts[1]
	}
	g := &sparkwing.Git{
		Branch: payload.PullRequest.Head.Ref,
		SHA:    payload.PullRequest.Head.SHA,
		Repo:   payload.Repository.FullName,
	}
	trigger.PullRequest = sparkwing.PullRequestFromEnv(triggerEnv)

	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:              runID,
		Pipeline:        pipeline,
		TriggerSource:   trigger.Source,
		TriggerUser:     trigger.User,
		TriggerEnv:      triggerEnv,
		GitBranch:       g.Branch,
		GitSHA:          g.SHA,
		Repo:            g.Repo,
		GithubOwner:     owner,
		GithubRepo:      repoName,
		WebhookDelivery: delivery,
		CreatedAt:       time.Now(),
	}); err != nil {
		if errors.Is(err, store.ErrDuplicateWebhookDelivery) {
			writeError(w, http.StatusConflict, errors.New("delivery already accepted"))
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist trigger: %w", err))
		return
	}

	pendingStatus := s.reserveGitHubCommitStatus(r.Context(), runID, "pending")
	dispatchAccepted := false
	defer func() { pendingStatus(dispatchAccepted) }()
	if err := s.dispatcher.Dispatch(r.Context(), RunRequest{
		RunID:    runID,
		Pipeline: pipeline,
		Trigger:  trigger,
		Git:      g,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dispatchAccepted = true

	s.logger.Info(
		"github pull_request webhook accepted",
		"pipeline", pipeline,
		"run_id", runID,
		"repo", payload.Repository.FullName,
		"pr", payload.Number,
		"action", payload.Action,
		"base", payload.PullRequest.Base.Ref,
		"head", payload.PullRequest.Head.Ref,
		"sha", payload.PullRequest.Head.SHA,
		"delivery", delivery,
	)
	writeJSON(w, http.StatusAccepted, triggerResp{
		RunID:  runID,
		Status: "dispatched",
	})
}

func verifyGitHubSignature(header string, body []byte, secret string) bool {
	expectedHex, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	expected, err := hex.DecodeString(expectedHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(expected, mac.Sum(nil))
}
