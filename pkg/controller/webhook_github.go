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
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// webhookBodyLimit caps the raw body size for /webhooks/github. 1 MiB
// is enough for any realistic GitHub push payload and keeps the HMAC
// verification read bounded.
const webhookBodyLimit = 1 << 20

// WithGitHubWebhookSecret installs the shared secret used to verify
// incoming GitHub webhook signatures. When empty, /webhooks/github
// returns 503. The same secret must be configured on every GitHub
// webhook targeting this controller.
func (s *Server) WithGitHubWebhookSecret(secret string) *Server {
	s.githubWebhookSecret = secret
	return s
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

// handleGitHubWebhook receives a GitHub webhook delivery for the
// pipeline named in the URL path. Auth is HMAC-SHA256 over the raw
// body; GitHub cannot carry a bearer token.
//
// Events: "ping" -> pong; "push" on a refs/heads/ branch -> enqueue
// a trigger; anything else -> 202 ignored. Tag pushes and branch
// deletions are ignored.
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.githubWebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable,
			errors.New("github webhook secret not configured on controller"))
		return
	}

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

	if !verifyGitHubSignature(r.Header.Get("X-Hub-Signature-256"), body, s.githubWebhookSecret) {
		writeError(w, http.StatusUnauthorized, errors.New("signature mismatch"))
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")

	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	case "push":
		s.handleGitHubPush(w, r, pipeline, delivery, body)
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
		Env: map[string]string{
			"GITHUB_DELIVERY":   delivery,
			"GITHUB_REPOSITORY": payload.Repository.FullName,
			"GITHUB_BEFORE":     payload.Before,
			"GITHUB_AFTER":      payload.After,
		},
	}
	owner, repoName := "", ""
	if parts := strings.SplitN(payload.Repository.FullName, "/", 2); len(parts) == 2 {
		owner, repoName = parts[0], parts[1]
	}
	g := &sparkwing.Git{
		Branch: branch,
		SHA:    payload.After,
		Repo:   repoName,
	}

	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:            runID,
		Pipeline:      pipeline,
		TriggerSource: trigger.Source,
		TriggerUser:   trigger.User,
		TriggerEnv:    trigger.Env,
		GitBranch:     g.Branch,
		GitSHA:        g.SHA,
		Repo:          g.Repo,
		GithubOwner:   owner,
		GithubRepo:    repoName,
		CreatedAt:     time.Now(),
	}); err != nil {
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

	s.logger.Info("github webhook accepted",
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

// verifyGitHubSignature checks the "sha256=<hex>" signature header
// against an HMAC-SHA256 of body with secret. Returns false on any
// shape mismatch so callers always reject ambiguous requests.
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
