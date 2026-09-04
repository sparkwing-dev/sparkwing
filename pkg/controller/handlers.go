package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var problems []string

	authState := "disabled"
	if s.auth != nil {
		authState = "enabled"
	}

	if _, err := s.store.ListRuns(r.Context(), store.RunFilter{Limit: 1}); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
			"auth":     authState,
			"problems": []string{"db: " + err.Error()},
		})
		return
	}

	if triggers, err := s.store.ListTriggers(r.Context(), store.TriggerFilter{
		Statuses: []string{"claimed"},
		Limit:    200,
	}); err == nil {
		stuck := 0
		cutoff := time.Now().Add(-30 * time.Minute)
		for _, t := range triggers {
			if t.ClaimedAt != nil && !t.ClaimedAt.IsZero() && t.ClaimedAt.Before(cutoff) {
				stuck++
			}
		}
		if stuck > 0 {
			problems = append(problems,
				fmt.Sprintf("triggers: %d claimed >30m without /done", stuck))
		}
	}

	if runs, err := s.store.ListRuns(r.Context(), store.RunFilter{
		Since: time.Now().Add(-24 * time.Hour),
		Limit: 500,
	}); err == nil && len(runs) >= 20 {
		success, failed := 0, 0
		for _, run := range runs {
			switch run.Status {
			case "success":
				success++
			case "failed", "cancelled":
				failed++
			}
		}
		if total := success + failed; total > 0 {
			rate := float64(success) / float64(total) * 100.0
			if rate < 80.0 {
				problems = append(problems,
					fmt.Sprintf("runs: %.0f%% success over %d (24h), %d failed",
						rate, total, failed))
			}
		}
	}

	resp := map[string]any{"status": "ok", "auth": authState}
	if len(problems) > 0 {
		resp["status"] = "degraded"
		resp["problems"] = problems
	}
	writeJSON(w, http.StatusOK, resp)
}

// safety: a stored sha reaches a runner's git as a revision argument, so only an object id may pass.
func validateGitSHA(sha string) error {
	if sha == "" || gitObjectSHA.MatchString(sha) {
		return nil
	}
	return errors.New("git.sha must be a 40-64 character hex object id")
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var body store.Run
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.ID == "" || body.Pipeline == "" || body.Status == "" {
		writeError(w, http.StatusBadRequest, errors.New("id, pipeline, status are required"))
		return
	}
	if err := validateGitSHA(body.GitSHA); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if p, ok := PrincipalFromContext(r.Context()); ok && !p.HasScope(ScopeAdmin) {
		held, err := s.ownsRun(r.Context(), body.ID, claimIdentity(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !held {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:      "claim_required",
				Principal: p.label(),
				Message:   "run " + body.ID + " is not claimed by this principal",
			})
			return
		}
		if !s.bindRunRepoToTrigger(w, r, &body) {
			return
		}
	}
	if err := s.store.CreateRun(r.Context(), body); err != nil {
		if errors.Is(err, store.ErrSecretInputHash) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// safety: the trigger names the repository a run's secrets resolve against, and the caller never does.
func (s *Server) bindRunRepoToTrigger(w http.ResponseWriter, r *http.Request, run *store.Run) bool {
	trig, err := s.store.GetTrigger(r.Context(), run.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if trig == nil {
		run.Repo, run.RepoURL, run.GithubOwner, run.GithubRepo = "", "", "", ""
		return true
	}
	if run.Repo != "" && run.Repo != trig.Repo {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("repo %q does not match the trigger's repository %q", run.Repo, trig.Repo))
		return false
	}
	run.Repo, run.RepoURL = trig.Repo, trig.RepoURL
	run.GithubOwner, run.GithubRepo = trig.GithubOwner, trig.GithubRepo
	return true
}

type finishRunReq struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// safety: the run row is terminal before the follow-ups run, and nothing else
// produces the terminal commit status for a finished run, so a client that
// goes away must not take them with it. Staying under the shutdown budget
// keeps a drain that starts mid-handler from ending one.
const finishRunFollowUpTimeout = controllerShutdownBudget - time.Second

func (s *Server) handleFinishRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var body finishRunReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Status == "" {
		writeError(w, http.StatusBadRequest, errors.New("status is required"))
		return
	}
	run, runErr := s.store.GetRun(r.Context(), runID)
	pipeline := ""
	if runErr == nil && run != nil {
		pipeline = run.Pipeline
	}
	otelutil.StampSpan(r.Context(), otelutil.SpanAttrs{
		RunID: runID, Pipeline: pipeline, Outcome: body.Status,
	})
	if err := s.store.FinishRun(r.Context(), runID, body.Status, body.Error); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	follow, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), finishRunFollowUpTimeout)
	defer cancel()
	if runErr == nil && run != nil {
		observeRunFinish(run.Pipeline, body.Status, time.Since(run.StartedAt))
		refreshed, rerr := s.store.GetRun(follow, runID)
		if rerr == nil {
			s.foldRunProfiles(follow, refreshed)
		}
	}
	s.reportGitHubCommitStatus(follow, runID, body.Status)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpdatePlanSnapshot(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	defer r.Body.Close()
	snapshot, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPlanSnapshotBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.UpdatePlanSnapshot(r.Context(), runID, snapshot); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	filter, parseErr := store.ParseRunFilterValidated(r.URL.Query())
	if parseErr != nil {
		writeError(w, http.StatusBadRequest, parseErr)
		return
	}
	runs, err := s.store.ListRuns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runs = store.RedactedRuns(runs)
	if runs == nil {
		runs = []*store.Run{}
	}
	w.Header().Set("X-Sparkwing-Run-Filter-Version", "1")
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

type secretValueGate func(*http.Request) bool

func (s *Server) secretValuesAllowed(r *http.Request) bool {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		// safety: with auth off the whole API is open, and redacting here would
		// feed runners "***" as a real argument value instead of failing.
		return s.authMiddleware().AuthDisabled()
	}
	if p.HasScope(ScopeAdmin) {
		return true
	}
	if !p.HasScope(ScopeNodesClaim) {
		return false
	}
	// safety: a runner reads a run's credentials only while it holds a claim on one of its nodes.
	held, err := s.store.PrincipalHoldsRunClaim(r.Context(), r.PathValue("id"), claimIdentity(r), time.Now())
	return err == nil && held
}

func loopbackSecretValuesAllowed(r *http.Request) bool {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		return false
	}
	return p.HasScope(ScopeAdmin) || p.HasScope(ScopeNodesClaim)
}

func dispatchEnvAllowed(r *http.Request) bool {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		return true
	}
	return p.HasScope(ScopeAdmin)
}

func dispatchForResponse(r *http.Request, d *store.NodeDispatch) *store.NodeDispatch {
	if d == nil || dispatchEnvAllowed(r) {
		return d
	}
	// safety: the captured environment is admin-only; every reader still sees which keys it lost.
	stripped := *d
	stripped.EnvJSON = nil
	return &stripped
}

func dispatchesForResponse(r *http.Request, in []*store.NodeDispatch) []*store.NodeDispatch {
	if dispatchEnvAllowed(r) {
		return in
	}
	out := make([]*store.NodeDispatch, 0, len(in))
	for _, d := range in {
		out = append(out, dispatchForResponse(r, d))
	}
	return out
}

func runForResponse(r *http.Request, run *store.Run, allowed secretValueGate) *store.Run {
	if includeHas(r.URL.Query().Get("include"), store.IncludeSecretValues) &&
		allowed(r) {
		return run
	}
	return store.RedactedRun(run)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if includeHas(r.URL.Query().Get("include"), "nodes") {
		nodes, err := s.store.ListNodes(r.Context(), runID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if nodes == nil {
			nodes = []*store.Node{}
		}
		for _, n := range nodes {
			if n.Deps == nil {
				n.Deps = []string{}
			}
		}
		steps, _ := s.store.ListNodeSteps(r.Context(), runID)
		approvals, _ := s.store.ListApprovalsForRun(r.Context(), runID)
		spawned, _ := s.store.ListSpawnedChildrenByRun(r.Context(), runID)
		decorated := api.DecorateNodes(nodes, run.PlanSnapshot, steps, approvals, spawned)
		writeJSON(w, http.StatusOK, map[string]any{"run": runForResponse(r, run, s.secretValuesAllowed), "nodes": decorated})
		return
	}
	writeJSON(w, http.StatusOK, runForResponse(r, run, s.secretValuesAllowed))
}

func includeHas(csv, target string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == target {
			return true
		}
	}
	return false
}

func (s *Server) handlePipelineLatest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("pipeline name required"))
		return
	}
	q := r.URL.Query()
	statuses := splitCSV(q.Get("status"))
	if len(statuses) == 0 {
		statuses = []string{"success"}
	}
	var maxAge time.Duration
	if v := q.Get("max_age"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_age: %w", err))
			return
		}
		if d < 0 {
			writeError(w, http.StatusBadRequest, errors.New("max_age must be >= 0"))
			return
		}
		maxAge = d
	}
	run, err := s.store.GetLatestRun(r.Context(), name, statuses, maxAge)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, store.RedactedRun(run))
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodes, err := s.store.ListNodes(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if nodes == nil {
		nodes = []*store.Node{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func splitCSV(s string) []string {
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var body store.Node
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.RunID = runID
	if body.NodeID == "" || body.Status == "" {
		writeError(w, http.StatusBadRequest, errors.New("node id and status are required"))
		return
	}
	if err := s.store.CreateNode(r.Context(), body); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleStartNode(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	if err := s.store.StartNode(r.Context(), runID, nodeID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type finishNodeReq struct {
	Outcome       string `json:"outcome"`
	Error         string `json:"error,omitempty"`
	Output        []byte `json:"output,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
}

func (s *Server) handleFinishNode(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body finishNodeReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Outcome == "" {
		writeError(w, http.StatusBadRequest, errors.New("outcome is required"))
		return
	}
	if err := s.store.FinishNodeWithReason(r.Context(), runID, nodeID, body.Outcome, body.Error, body.Output, body.FailureReason, body.ExitCode); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type updateDepsReq struct {
	Deps []string `json:"deps"`
}

func (s *Server) handleUpdateNodeDeps(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body updateDepsReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.UpdateNodeDeps(r.Context(), runID, nodeID, body.Deps); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type appendEventReq struct {
	NodeID  string `json:"node_id,omitempty"`
	Kind    string `json:"kind"`
	Payload []byte `json:"payload,omitempty"`
}

type appendEventResp struct {
	Seq int64 `json:"seq"`
}

func (s *Server) handleAppendEvent(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var body appendEventReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Kind == "" {
		writeError(w, http.StatusBadRequest, errors.New("kind is required"))
		return
	}
	seq, err := s.store.AppendEvent(r.Context(), runID, body.NodeID, body.Kind, body.Payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, appendEventResp{Seq: seq})
}

type triggerReqMeta struct {
	Source string            `json:"source,omitempty"`
	User   string            `json:"user,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

type triggerReqGit struct {
	Branch      string `json:"branch,omitempty"`
	SHA         string `json:"sha,omitempty"`
	Repo        string `json:"repo,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	GithubOwner string `json:"github_owner,omitempty"`
	GithubRepo  string `json:"github_repo,omitempty"`
}

type triggerReq struct {
	Pipeline     string            `json:"pipeline"`
	Args         map[string]string `json:"args,omitempty"`
	Trigger      triggerReqMeta    `json:"trigger,omitempty"`
	Git          triggerReqGit     `json:"git,omitempty"`
	ParentRunID  string            `json:"parent_run_id,omitempty"`
	ParentNodeID string            `json:"parent_node_id,omitempty"`
	RetryOf      string            `json:"retry_of,omitempty"`
}

type triggerResp struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// safety: every other trigger_env key a run reads is controller-written, so an inbound copy forges it.
var submittedTriggerEnvKeys = map[string]bool{
	"GITHUB_REPOSITORY":          true,
	sparkwing.EnvGitHubEventName: true,
	sparkwing.EnvPRNumber:        true,
	sparkwing.EnvPRAction:        true,
	sparkwing.EnvPRBaseRef:       true,
	sparkwing.EnvPRBaseSHA:       true,
	sparkwing.EnvPRHeadRef:       true,
	sparkwing.EnvPRHeadSHA:       true,
	"SPARKWING_START_AT":         true,
	"SPARKWING_STOP_AT":          true,
	"SPARKWING_ONLY":             true,
	"SPARKWING_DRY_RUN":          true,
	"SPARKWING_NO_CACHE":         true,
}

var githubProvenanceEnvKeys = map[string]bool{
	sparkwing.EnvGitHubEventName: true,
	sparkwing.EnvPRNumber:        true,
	sparkwing.EnvPRAction:        true,
	sparkwing.EnvPRBaseRef:       true,
	sparkwing.EnvPRBaseSHA:       true,
	sparkwing.EnvPRHeadRef:       true,
	sparkwing.EnvPRHeadSHA:       true,
}

var githubRepoSlug = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// safety: this key wins over git.repo_url on the runner, so it stays a slug and never becomes a URL.
func validateSubmittedRepoSlug(env map[string]string) error {
	repo := env["GITHUB_REPOSITORY"]
	if repo == "" || githubRepoSlug.MatchString(repo) {
		return nil
	}
	return errors.New("trigger.env GITHUB_REPOSITORY must be an owner/name slug")
}

// safety: the commit-status reporter spends the controller's GitHub token on whatever these name.
func refuseForgedGitHubProvenance(ctx context.Context, source string, env map[string]string) error {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.HasScope(ScopeAdmin) {
		return nil
	}
	if source == "github" {
		return errors.New(`trigger.source "github" is reserved for the verified GitHub webhook`)
	}
	for key := range env {
		if githubProvenanceEnvKeys[key] {
			return fmt.Errorf("trigger.env %s is reserved for the verified GitHub webhook", key)
		}
	}
	return nil
}

func sanitizeTriggerEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(env))
	for key, value := range env {
		// safety: an inbound key outside this set is either a credential or a forged provenance marker.
		if !submittedTriggerEnvKeys[key] {
			continue
		}
		// safety: trigger_env is served whole to every triggers.read principal, so no credential-named key may persist.
		if envredact.CredentialName(key) {
			continue
		}
		cleaned[key] = value
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func (s *Server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	var body triggerReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Pipeline == "" {
		writeError(w, http.StatusBadRequest, errors.New("pipeline is required"))
		return
	}

	if body.Trigger.Source == "" {
		writeError(w, http.StatusBadRequest, errors.New("trigger.source is required"))
		return
	}
	if err := validateSubmittedRepoSlug(body.Trigger.Env); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := refuseForgedGitHubProvenance(r.Context(), body.Trigger.Source, body.Trigger.Env); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err := validateGitSHA(body.Git.SHA); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Git.RepoURL != "" {
		// safety: the repo URL becomes a clone target on every runner, so hold it to the gitcache rules.
		validated, verr := sourceurl.ValidateCloneURL(body.Git.RepoURL)
		if verr != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("git.repo_url: %w", verr))
			return
		}
		body.Git.RepoURL = validated
	}

	runID := newRunID()
	repoInherited := body.ParentRunID != "" && body.Git.Repo == ""

	if body.ParentRunID != "" {
		ancestors, err := s.store.GetRunAncestorPipelines(r.Context(), body.ParentRunID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("ancestor walk: %w", err))
			return
		}
		parent, perr := s.store.GetRun(r.Context(), body.ParentRunID)
		if perr != nil {
			if errors.Is(perr, store.ErrNotFound) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("parent_run_id %s not found", body.ParentRunID))
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Errorf("get parent run: %w", perr))
			return
		}
		chain := append([]string{parent.Pipeline}, ancestors...)
		for _, p := range chain {
			if p == body.Pipeline {
				trace := body.Pipeline
				for i := range chain {
					trace += " <- " + chain[i]
				}
				writeError(w, http.StatusConflict,
					fmt.Errorf("cycle: %s would re-enter itself (%s)", body.Pipeline, trace))
				return
			}
		}

		if body.Git.Repo == "" {
			if strings.HasPrefix(parent.TriggerSource, "pipeline-working-tree@") {
				body.Trigger.Source = parent.TriggerSource
			}
			body.Git.Repo = parent.Repo
			body.Git.RepoURL = parent.RepoURL
			if body.Git.Branch == "" {
				body.Git.Branch = parent.GitBranch
			}
			if body.Git.SHA == "" {
				body.Git.SHA = parent.GitSHA
			}
			if body.Git.GithubOwner == "" {
				body.Git.GithubOwner = parent.GithubOwner
			}
			if body.Git.GithubRepo == "" {
				body.Git.GithubRepo = parent.GithubRepo
			}
		}
	}

	triggerEnv := sanitizeTriggerEnv(body.Trigger.Env)

	now := time.Now()
	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:            runID,
		Pipeline:      body.Pipeline,
		Args:          body.Args,
		TriggerSource: body.Trigger.Source,
		TriggerUser:   body.Trigger.User,
		TriggerEnv:    triggerEnv,
		GitBranch:     body.Git.Branch,
		GitSHA:        body.Git.SHA,
		Repo:          body.Git.Repo,
		RepoURL:       body.Git.RepoURL,
		GithubOwner:   body.Git.GithubOwner,
		GithubRepo:    body.Git.GithubRepo,
		CreatedAt:     now,
		ParentRunID:   body.ParentRunID,
		ParentNodeID:  body.ParentNodeID,
		RetryOf:       body.RetryOf,
		RepoInherited: repoInherited,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist trigger: %w", err))
		return
	}

	if err := s.store.CreateRun(r.Context(), store.Run{
		ID:            runID,
		Pipeline:      body.Pipeline,
		Status:        "pending",
		TriggerSource: body.Trigger.Source,
		GitBranch:     body.Git.Branch,
		GitSHA:        body.Git.SHA,
		Args:          body.Args,
		ParentRunID:   body.ParentRunID,
		Repo:          body.Git.Repo,
		RepoURL:       body.Git.RepoURL,
		GithubOwner:   body.Git.GithubOwner,
		GithubRepo:    body.Git.GithubRepo,
		RetryOf:       body.RetryOf,
		CreatedAt:     now,
		StartedAt:     now,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist run: %w", err))
		return
	}

	if err := s.dispatcher.Dispatch(r.Context(), RunRequest{
		RunID:    runID,
		Pipeline: body.Pipeline,
		Args:     body.Args,
		Trigger: sparkwing.TriggerInfo{
			Source: body.Trigger.Source,
			User:   body.Trigger.User,
		},
		Git: &sparkwing.Git{
			Branch:  body.Git.Branch,
			SHA:     body.Git.SHA,
			Repo:    body.Git.Repo,
			RepoURL: body.Git.RepoURL,
		},
		ParentRunID: body.ParentRunID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusAccepted, triggerResp{
		RunID:  runID,
		Status: "dispatched",
	})
}

type heartbeatResp struct {
	CancelRequested bool `json:"cancel_requested"`
}

// safety: scope alone says a principal may work triggers, not which trigger,
// so ending or renewing one is bound to the claimant its row records. Admin
// bypasses, and an unauthenticated server has no claimant to bind to.
func (s *Server) claimedTrigger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok || p.HasScope(ScopeAdmin) {
			next.ServeHTTP(w, r)
			return
		}
		id := r.PathValue("id")
		holder, err := s.store.TriggerClaimant(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// safety: a row nobody holds is a reaped claim, and a worker's heartbeat
		// loop stops on not-found; answering forbidden there would keep it
		// retrying until its silence window terminates the whole consumer.
		if holder.TokenPrefix == "" {
			writeError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		if holder != claimIdentity(r) {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:      "claim_required",
				Principal: p.label(),
				Message:   "trigger " + id + " is claimed by another principal",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cancelled, err := s.store.HeartbeatTrigger(r.Context(), id, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, heartbeatResp{CancelRequested: cancelled})
}

func (s *Server) handleFinishTrigger(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.FinishTrigger(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTriggers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.TriggerFilter{}
	if v := q.Get("status"); v != "" {
		filter.Statuses = splitCSV(v)
	}
	if v := q.Get("pipeline"); v != "" {
		filter.Pipelines = splitCSV(v)
	}
	if v := q.Get("repo"); v != "" {
		filter.Repo = v
	}
	if v := q.Get("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			filter.Limit = min(n, store.MaxRunListLimit)
		}
	}
	trigs, err := s.store.ListTriggers(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if trigs == nil {
		trigs = []*store.Trigger{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggers": trigs})
}

func (s *Server) handleFindSpawnedChildTrigger(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	parentRunID := q.Get("parent_run_id")
	parentNodeID := q.Get("parent_node_id")
	pipeline := q.Get("pipeline")
	if parentRunID == "" || parentNodeID == "" || pipeline == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parent_run_id, parent_node_id, pipeline are all required"))
		return
	}
	id, err := s.store.FindSpawnedChildTriggerID(r.Context(), parentRunID, parentNodeID, pipeline)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"run_id": id})
}

func (s *Server) handleListPendingTriggersForParent(w http.ResponseWriter, r *http.Request) {
	parent := r.PathValue("id")
	if parent == "" {
		writeError(w, http.StatusBadRequest, errors.New("parent run id is required"))
		return
	}
	ids, err := s.store.ListPendingTriggersForParent(r.Context(), parent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"trigger_ids": ids})
}

type claimSpecificTriggerReq struct {
	LeaseNanos int64 `json:"lease_nanos,omitempty"`
}

func (s *Server) handleClaimSpecificTrigger(w http.ResponseWriter, r *http.Request) {
	var body claimSpecificTriggerReq
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	lease := time.Duration(body.LeaseNanos)
	if lease <= 0 {
		lease = store.DefaultLeaseDuration
	}
	t, err := s.store.ClaimSpecificTriggerFor(r.Context(), r.PathValue("id"), claimIdentity(r), lease)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleGetTrigger(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tr, err := s.store.GetTrigger(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tr)
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.RequestCancel(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteRun(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type claimTriggerReq struct {
	Pipelines      []string `json:"pipelines,omitempty"`
	TriggerSources []string `json:"trigger_sources,omitempty"`
}

func (s *Server) handleClaimTrigger(w http.ResponseWriter, r *http.Request) {
	var body claimTriggerReq
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	t, err := s.store.ClaimNextTriggerFor(r.Context(), claimIdentity(r), 0, body.Pipelines, body.TriggerSources)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func newRunID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("run-%s-%s", ts, hex.EncodeToString(suffix[:]))
}

const (
	maxJSONBody = 1 << 20
	// safety: a secret value is caller data, so it gets its own ceiling
	// rather than an exemption from the shared decode path.
	maxSecretJSONBody = 8 << 20
	// safety: a plan snapshot is stored verbatim rather than decoded, so it
	// needs a ceiling of its own; it matches the store's envelope ceiling
	// because both bound one blob on a run's row.
	maxPlanSnapshotBody = store.MaxNodeDispatchEnvelope
)

func decodeJSON(r *http.Request, v any) error {
	return decodeJSONLimit(r, v, maxJSONBody)
}

// safety: a chunked body reports no content length, so a route whose body is
// optional reads it whenever there is one and treats only an empty body as
// absent; gating on a positive length dropped what a streaming client sent.
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if err := decodeJSON(r, v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func decodeJSONLimit(r *http.Request, v any, limit int64) error {
	defer r.Body.Close()
	// safety: an application/json body forces a CORS preflight, so a page
	// on another site cannot post one as a simple request.
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		return err
	}
	body := http.MaxBytesReader(nil, r.Body, limit)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func requireJSONContentType(header string) error {
	if header == "" {
		return errors.New("content-type application/json required")
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return fmt.Errorf("content-type %q: %w", header, err)
	}
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return fmt.Errorf("content-type %q: application/json required", mediaType)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	n, err := s.store.GetNode(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleGetNodeOutput(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	n, err := s.store.GetNode(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if n.Status != "done" {
		writeError(w, http.StatusConflict, fmt.Errorf("node %s/%s not finished (status=%s)", runID, nodeID, n.Status))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if len(n.Output) > 0 {
		_, _ = w.Write(n.Output)
	} else {
		_, _ = w.Write([]byte("null"))
	}
}

func (s *Server) handleWriteNodeDispatch(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var d store.NodeDispatch
	if err := decodeJSON(r, &d); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d.RunID = runID
	d.NodeID = nodeID
	if err := s.store.WriteNodeDispatch(r.Context(), d); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleGetNodeDispatch(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	seq := -1
	if v := r.URL.Query().Get("seq"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid seq: %w", err))
			return
		}
		seq = n
	}
	d, err := s.store.GetNodeDispatch(r.Context(), runID, nodeID, seq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, dispatchForResponse(r, d))
}

func (s *Server) handleListNodeDispatches(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	out, err := s.store.ListNodeDispatches(r.Context(), runID, nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []*store.NodeDispatch{}
	}
	writeJSON(w, http.StatusOK, dispatchesForResponse(r, out))
}

type claimNodeReq struct {
	HolderID  string         `json:"holder_id"`
	LeaseSecs int            `json:"lease_secs,omitempty"`
	Labels    []string       `json:"labels,omitempty"`
	Headroom  *claimHeadroom `json:"headroom,omitempty"`
}

type claimHeadroom struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
	QueueDepth  int     `json:"queue_depth"`
}

func (s *Server) recordAdvertisedHeadroom(holderID string, h *claimHeadroom) {
	if h == nil {
		return
	}
	name, _ := holderName(holderID)
	s.runnerHeadroom.record(name, runnerHeadroom{
		Cores:       h.Cores,
		MemoryBytes: h.MemoryBytes,
		QueueDepth:  h.QueueDepth,
		UpdatedAt:   time.Now(),
	})
}

func (s *Server) handleClaimNode(w http.ResponseWriter, r *http.Request) {
	var body claimNodeReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.HolderID == "" {
		writeError(w, http.StatusBadRequest, errors.New("holder_id is required"))
		return
	}
	s.recordAdvertisedHeadroom(body.HolderID, body.Headroom)
	lease := time.Duration(body.LeaseSecs) * time.Second
	n, err := s.store.ClaimNextReadyNode(r.Context(), claimIdentity(r), body.HolderID, lease, body.Labels)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	pipeline := ""
	if run, err := s.store.GetRun(r.Context(), n.RunID); err == nil && run != nil {
		pipeline = run.Pipeline
	}
	observeNodeClaim(pipeline)
	otelutil.StampSpan(r.Context(), otelutil.SpanAttrs{
		RunID: n.RunID, NodeID: n.NodeID, Pipeline: pipeline,
	})
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleMarkNodeReady(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	if err := s.store.MarkNodeReady(r.Context(), runID, nodeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type revokeResp struct {
	Revoked bool `json:"revoked"`
}

func (s *Server) handleRevokeNodeReady(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	ok, err := s.store.RevokeNodeReady(r.Context(), runID, nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, revokeResp{Revoked: ok})
}

func (s *Server) handleHeartbeatNodeClaim(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body claimNodeReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.HolderID == "" {
		writeError(w, http.StatusBadRequest, errors.New("holder_id is required"))
		return
	}
	s.recordAdvertisedHeadroom(body.HolderID, body.Headroom)
	lease := time.Duration(body.LeaseSecs) * time.Second
	if err := s.store.HeartbeatNodeClaim(r.Context(), runID, nodeID, claimIdentity(r), body.HolderID, lease); err != nil {
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpdateNodeActivity(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		Detail string `json:"detail"`
	}
	_ = decodeJSON(r, &body)
	if err := s.store.UpdateNodeActivity(r.Context(), runID, nodeID, body.Detail); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAppendNodeAnnotation(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.AppendNodeAnnotation(r.Context(), runID, nodeID, body.Message); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetNodeSummary(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		Markdown string `json:"markdown"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetNodeSummary(r.Context(), runID, nodeID, body.Markdown); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetNodeArtifactManifest(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		ManifestDigest string `json:"manifest_digest"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetNodeArtifactManifest(r.Context(), runID, nodeID, body.ManifestDigest); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartNodeStep(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		StepID string `json:"step_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := s.store.StartNodeStep(r.Context(), runID, nodeID, body.StepID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFinishNodeStep(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		StepID string `json:"step_id"`
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if body.Status != store.StepPassed && body.Status != store.StepFailed {
		writeError(w, http.StatusBadRequest, errors.New("status must be passed or failed"))
		return
	}
	if err := s.store.FinishNodeStep(r.Context(), runID, nodeID, body.StepID, body.Status); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSkipNodeStep(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		StepID string `json:"step_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := s.store.SkipNodeStep(r.Context(), runID, nodeID, body.StepID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAppendStepAnnotation(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		StepID  string `json:"step_id"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := s.store.AppendStepAnnotation(r.Context(), runID, nodeID, body.StepID, body.Message); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetStepSummary(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		StepID   string `json:"step_id"`
		Markdown string `json:"markdown"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := s.store.SetStepSummary(r.Context(), runID, nodeID, body.StepID, body.Markdown); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListNodeSteps(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	steps, err := s.store.ListNodeSteps(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if steps == nil {
		steps = []*store.NodeStep{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
}

func (s *Server) handleTouchNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	if err := s.store.TouchNodeHeartbeat(r.Context(), runID, nodeID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTouchRunHeartbeat(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, err := s.store.GetRun(r.Context(), runID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.TouchRunHeartbeat(r.Context(), runID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateDebugPause(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var body store.DebugPause
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.RunID = runID
	if body.NodeID == "" || body.Reason == "" {
		writeError(w, http.StatusBadRequest, errors.New("node_id and reason are required"))
		return
	}
	if err := s.store.CreateDebugPause(r.Context(), body); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var afterSeq int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid after: %w", err))
			return
		}
		afterSeq = n
	}
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid limit"))
			return
		}
		limit = min(n, store.MaxRunListLimit)
	}
	events, err := s.store.ListEventsAfter(r.Context(), runID, afterSeq, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if events == nil {
		events = []store.Event{}
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleListDebugPauses(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	pauses, err := s.store.ListDebugPauses(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if pauses == nil {
		pauses = []*store.DebugPause{}
	}
	writeJSON(w, http.StatusOK, pauses)
}

func (s *Server) handleGetActiveDebugPause(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	p, err := s.store.GetActiveDebugPause(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleReleaseDebugPause(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		ReleaseKind string `json:"release_kind"`
	}
	_ = decodeJSON(r, &body)
	if body.ReleaseKind == "" {
		body.ReleaseKind = store.PauseReleaseManual
	}
	releasedBy := auditPrincipal(r)
	if err := s.store.ReleaseDebugPause(r.Context(), runID, nodeID, releasedBy, body.ReleaseKind); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func auditPrincipal(r *http.Request) string {
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil && p.Name != "" {
		return p.Name
	}
	return "anonymous"
}

func (s *Server) handleSetNodeStatus(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Status == "" {
		writeError(w, http.StatusBadRequest, errors.New("status is required"))
		return
	}
	if err := s.store.SetNodeStatus(r.Context(), runID, nodeID, body.Status); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
