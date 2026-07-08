package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// handleHealth is the liveness probe. Returns 200 when the process is
// up; component failures land in problems[] so callers can surface them
// without a blanket outage banner. Only DB-unreachable flips to 503.
//
// Response: {"status": "ok" | "degraded", "problems": ["comp: detail"]}.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var problems []string

	if _, err := s.store.ListRuns(r.Context(), store.RunFilter{Limit: 1}); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
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

	resp := map[string]any{"status": "ok"}
	if len(problems) > 0 {
		resp["status"] = "degraded"
		resp["problems"] = problems
	}
	writeJSON(w, http.StatusOK, resp)
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
	if err := s.store.CreateRun(r.Context(), body); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

type finishRunReq struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

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
	if runErr == nil && run != nil {
		observeRunFinish(run.Pipeline, body.Status, time.Since(run.StartedAt))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdatePlanSnapshot accepts raw JSON bytes as the snapshot
// payload. Content-Type is ignored; the store treats it as opaque.
func (s *Server) handleUpdatePlanSnapshot(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	defer r.Body.Close()
	snapshot, err := io.ReadAll(r.Body)
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

// handleListRuns serves the dashboard/CLI read path. Filter parsing
// shared with the laptop controller via store.ParseRunFilter.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	filter := store.ParseRunFilter(r.URL.Query())
	runs, err := s.store.ListRuns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if runs == nil {
		runs = []*store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleGetRun serves a single run by id. Default response is the raw
// store.Run JSON. With ?include=nodes it returns {run, nodes}.
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
		writeJSON(w, http.StatusOK, map[string]any{"run": run, "nodes": decorated})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// includeHas reports whether the comma-separated `include` query
// parameter contains target. Whitespace is trimmed; case-sensitive.
func includeHas(csv, target string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == target {
			return true
		}
	}
	return false
}

// handlePipelineLatest serves the cross-pipeline-ref read endpoint.
// Accepts ?status=success,failed (csv, default success), ?max_age=1h.
// Returns the matching Run JSON or 404 when nothing matches.
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
	writeJSON(w, http.StatusOK, run)
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

// splitCSV is a tolerant comma-separated parser for query params.
// Empty segments are dropped; whitespace is trimmed.
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
	Output        []byte `json:"output,omitempty"` // JSON-encoded
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

// triggerReqMeta is the trigger block on POST /api/v1/triggers
// bodies. Decoupled from the SDK's sparkwing.TriggerInfo: Env
// carries operational metadata (GITHUB_DELIVERY, GITHUB_REPOSITORY,
// range-resume markers, ...) onto the persisted store.Trigger row
// but is not surfaced to step bodies. Step-body code reads
// trigger-supplied values via the pipeline's typed Config struct
// (declared under the trigger's values: block in pipelines.yaml).
type triggerReqMeta struct {
	Source string            `json:"source,omitempty"`
	User   string            `json:"user,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// triggerReqGit mirrors client.GitMeta on the wire. Field names MUST
// match client.GitMeta JSON tags exactly.
type triggerReqGit struct {
	Branch      string `json:"branch,omitempty"`
	SHA         string `json:"sha,omitempty"`
	Repo        string `json:"repo,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	GithubOwner string `json:"github_owner,omitempty"`
	GithubRepo  string `json:"github_repo,omitempty"`
}

type triggerReq struct {
	Pipeline      string                  `json:"pipeline"`
	Args          map[string]string       `json:"args,omitempty"`
	Trigger       triggerReqMeta          `json:"trigger,omitempty"` // see triggerReqMeta below
	Git           triggerReqGit           `json:"git,omitempty"`
	PlanAdmission triggerReqPlanAdmission `json:"plan_admission,omitempty"`
	// ParentRunID identifies the run that spawned this trigger via
	// sparkwing.RunAndAwait; the controller walks the parent
	// chain to reject cycles before persisting.
	ParentRunID string `json:"parent_run_id,omitempty"`
	// ParentNodeID identifies which node of the parent run did the
	// spawning, so a retry of the parent can locate the prior child
	// by (parent_run_id, parent_node_id, pipeline) and chain retry_of.
	ParentNodeID string `json:"parent_node_id,omitempty"`
	// RetryOf, when non-empty, marks this trigger as a retry of the
	// named run; skip-passed rehydration uses it to seed outputs from
	// the prior run's node rows.
	RetryOf string `json:"retry_of,omitempty"`
}

type triggerReqPlanAdmission struct {
	Key      string `json:"key,omitempty"`
	HolderID string `json:"holder_id,omitempty"`
}

type triggerResp struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

const (
	triggerEnvPlanAdmissionKey      = "SPARKWING_PLAN_ADMISSION_KEY"
	triggerEnvPlanAdmissionHolderID = "SPARKWING_PLAN_ADMISSION_HOLDER_ID"
	triggerEnvPlanAdmissions        = "SPARKWING_PLAN_ADMISSIONS"
)

func sanitizeTriggerEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(env))
	for key, value := range env {
		switch key {
		case triggerEnvPlanAdmissionKey, triggerEnvPlanAdmissionHolderID, triggerEnvPlanAdmissions:
			continue
		default:
			cleaned[key] = value
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func (s *Server) validatePlanAdmission(ctx context.Context, parentRunID string, admission triggerReqPlanAdmission) (map[string]string, error) {
	if parentRunID == "" {
		return nil, errors.New("plan_admission requires parent_run_id")
	}
	if admission.Key == "" || admission.HolderID == "" {
		return nil, errors.New("plan_admission requires key and holder_id")
	}
	holder, err := s.store.ActiveConcurrencyHolder(ctx, admission.Key, admission.HolderID, time.Now())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("plan_admission holder %q is not active for key %q", admission.HolderID, admission.Key)
		}
		return nil, fmt.Errorf("plan_admission validate holder: %w", err)
	}
	expectedHolderID := holder.RunID + "/-"
	if holder.NodeID != "" || admission.HolderID != expectedHolderID {
		return nil, fmt.Errorf("plan_admission holder %q is not a plan holder for run %q", admission.HolderID, holder.RunID)
	}
	if ok, err := s.runIsSelfOrAncestor(ctx, parentRunID, holder.RunID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("plan_admission holder %q belongs to run %q, not parent run %q or its ancestors",
			admission.HolderID, holder.RunID, parentRunID)
	}
	return map[string]string{
		triggerEnvPlanAdmissionKey:      admission.Key,
		triggerEnvPlanAdmissionHolderID: admission.HolderID,
	}, nil
}

func (s *Server) runIsSelfOrAncestor(ctx context.Context, runID, candidateAncestorRunID string) (bool, error) {
	if candidateAncestorRunID == "" {
		return false, nil
	}
	currentRunID := runID
	const maxDepth = 64
	for range maxDepth {
		if currentRunID == "" {
			return false, nil
		}
		if currentRunID == candidateAncestorRunID {
			return true, nil
		}
		run, err := s.store.GetRun(ctx, currentRunID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return false, fmt.Errorf("parent_run_id %s not found", currentRunID)
			}
			return false, fmt.Errorf("get parent run %s: %w", currentRunID, err)
		}
		currentRunID = run.ParentRunID
	}
	return false, fmt.Errorf("parent_run_id %s ancestor chain exceeds %d", runID, maxDepth)
}

// handleTrigger is the external intake for a new run. Persists the
// trigger to the queue then notifies the Dispatcher. Returns 202 with
// the run ID immediately so webhooks can respond within seconds.
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
	runID := newRunID()

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
	var inheritedPlanConcurrencyKey, inheritedPlanConcurrencyHolderID string
	if body.PlanAdmission.Key != "" || body.PlanAdmission.HolderID != "" {
		admissionEnv, err := s.validatePlanAdmission(r.Context(), body.ParentRunID, body.PlanAdmission)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if triggerEnv == nil {
			triggerEnv = map[string]string{}
		}
		for key, value := range admissionEnv {
			triggerEnv[key] = value
		}
		inheritedPlanConcurrencyKey = body.PlanAdmission.Key
		inheritedPlanConcurrencyHolderID = body.PlanAdmission.HolderID
	}

	// The trigger ID doubles as the eventual run ID.
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
		ParentRunID:                      body.ParentRunID,
		InheritedPlanConcurrencyKey:      inheritedPlanConcurrencyKey,
		InheritedPlanConcurrencyHolderID: inheritedPlanConcurrencyHolderID,
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

// handleHeartbeat extends the lease on a claimed trigger and reports
// whether the operator has requested cancellation. 404 when the
// trigger is already gone -- the worker should stop.
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

// handleFinishTrigger flips a trigger to 'done'. Idempotent.
func (s *Server) handleFinishTrigger(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.FinishTrigger(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListTriggers serves the operator read path for queued,
// in-flight, and done triggers.
//
// Query params:
//   - status: csv of pending|claimed|done
//   - pipeline: csv of pipeline names
//   - repo: match GITHUB_REPOSITORY on trigger_env
//   - limit: int (default 20)
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
			filter.Limit = n
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

// handleFindSpawnedChildTrigger returns the most-recent child trigger
// id created at (parent_run_id, parent_node_id) targeting `pipeline`.
// Required query params: parent_run_id, parent_node_id, pipeline.
// Returns 200 + {"run_id": ""} when no match.
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

// handleGetTrigger returns one trigger row by id.
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

// handleCancelRun records an operator cancellation request for the
// run. Idempotent: subsequent calls for the same run are no-ops.
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

// handleDeleteRun removes one run (with nodes/events via FK cascade)
// and its trigger. Idempotent: a missing run returns 204.
func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteRun(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// claimTriggerReq is the optional body for POST /triggers/claim.
// Empty body claims any pending trigger.
type claimTriggerReq struct {
	// Pipelines restricts candidates to the named pipelines. Empty =
	// no restriction.
	Pipelines []string `json:"pipelines,omitempty"`
	// TriggerSources restricts candidates by trigger_source value.
	// AND-semantics with Pipelines. Empty = no restriction.
	TriggerSources []string `json:"trigger_sources,omitempty"`
}

// handleClaimTrigger atomically claims the oldest pending trigger and
// returns the full record. 204 when the queue is empty. 400 on
// malformed body.
func (s *Server) handleClaimTrigger(w http.ResponseWriter, r *http.Request) {
	var body claimTriggerReq
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	t, err := s.store.ClaimNextTriggerFor(r.Context(), 0, body.Pipelines, body.TriggerSources)
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

// newRunID produces a sortable, human-readable identifier matching
// the shape the orchestrator uses, so the trigger handler can return
// a run ID before the orchestrator starts.
func newRunID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("run-%s-%s", ts, hex.EncodeToString(suffix[:]))
}

// decodeJSON reads the request body as JSON into v. Enforces a 1 MiB
// ceiling to avoid unbounded memory on malformed clients.
func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	body := http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
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

// handleGetNode returns a single node row.
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

// handleGetNodeOutput returns just the raw output JSON for one node,
// avoiding the wrapper overhead on a hot path (every Ref[T].Get).
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

// handleWriteNodeDispatch persists a dispatch snapshot. The path's
// runID/nodeID override the body's so a caller can't write snapshots
// for a different node.
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

// handleGetNodeDispatch returns a single dispatch snapshot. Optional
// ?seq=N selects a specific attempt; omitted means most-recent.
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
	writeJSON(w, http.StatusOK, d)
}

// handleListNodeDispatches returns every dispatch snapshot for the
// node, ordered oldest-first.
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
	writeJSON(w, http.StatusOK, out)
}

type claimNodeReq struct {
	HolderID  string `json:"holder_id"`
	LeaseSecs int    `json:"lease_secs,omitempty"`
	// Labels advertised by the claiming runner. The store filters
	// candidate nodes whose needs_labels is a subset of this set
	// (AND). Empty/absent => only unlabeled nodes are claimable.
	Labels []string `json:"labels,omitempty"`
}

// handleClaimNode atomically hands the oldest ready, unclaimed node
// to the caller. 204 on empty queue so pool runners can back off
// without treating it as an error.
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
	lease := time.Duration(body.LeaseSecs) * time.Second
	n, err := s.store.ClaimNextReadyNode(r.Context(), body.HolderID, lease, body.Labels)
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

// handleMarkNodeReady sets ready_at on a node. Idempotent; multiple
// calls keep the original ready_at so FIFO ordering is stable across
// revoke/retry cycles.
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

// handleRevokeNodeReady atomically nulls ready_at iff the node is not
// currently claimed. False means a pod beat us to it.
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

// handleHeartbeatNodeClaim extends a node claim's lease. 409 when
// the caller isn't the current claim holder.
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
	lease := time.Duration(body.LeaseSecs) * time.Second
	if err := s.store.HeartbeatNodeClaim(r.Context(), runID, nodeID, body.HolderID, lease); err != nil {
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateNodeActivity writes the runner-reported status_detail
// and bumps last_heartbeat. Body is {"detail":"<string>"}.
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

// handleAppendNodeAnnotation appends one persistent summary string
// to the node's annotations list. Body is {"message":"<string>"}.
// Driven by sparkwing.Annotate() inside step bodies.
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

// handleSetNodeSummary overwrites the node's markdown run summary.
// Body is {"markdown":"<string>"}. Driven by sparkwing.Summary()
// emitted outside any step body. Last write wins.
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

// handleSetNodeArtifactManifest records the content-addressed digest of
// the node's published-artifact manifest. Body is
// {"manifest_digest":"<string>"}. Last write wins.
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

// handleStartNodeStep records the running transition for one inner
// Work step. Server stamps started_at; idempotent so retried POSTs
// don't reset the clock.
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

// handleFinishNodeStep records the terminal status of a step.
// Accepts "passed" or "failed".
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

// handleSkipNodeStep records a step that never ran (skipIf guard,
// dry-run gap, etc.).
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

// handleAppendStepAnnotation appends one persistent summary string
// to a step's annotations list. Body is {"step_id":"...","message":"..."}.
// Driven by sparkwing.Annotate() called from inside a step body
// (the active step is captured via the rec.Step envelope field).
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

// handleSetStepSummary overwrites a step's markdown run summary.
// Body is {"step_id":"...","markdown":"..."}. Driven by
// sparkwing.Summary() emitted inside a step body. Last write wins.
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

// handleListNodeSteps returns every step row for one run as one
// flat slice. Callers bucket by node_id client-side; the rows ship
// in (node_id, started_at) order so that's cheap.
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

// handleTouchNodeHeartbeat bumps last_heartbeat without touching
// status_detail. Runners call this on a ticker while executing.
func (s *Server) handleTouchNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	if err := s.store.TouchNodeHeartbeat(r.Context(), runID, nodeID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTouchRunHeartbeat bumps last_heartbeat_at on the run row.
// Orchestrators call this on a ticker while the run is active so the
// controller's reaper can detect a fully-orphaned dispatcher and
// flip the run to failed instead of leaving it pinned at 'running'.
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

// handleListEvents returns events for a run with seq > ?after=N
// (default 0), capped by ?limit=N (default 500). Always returns a
// JSON array (never null) so the client can treat an empty tail as
// "nothing new yet".
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
		limit = n
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
	releasedBy := releasedByFromAuth(r)
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

// releasedByFromAuth derives the audit identity for a debug-pause
// release from the authenticated principal. Returns "anonymous" when
// auth is disabled so the audit row is still meaningful.
func releasedByFromAuth(r *http.Request) string {
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
