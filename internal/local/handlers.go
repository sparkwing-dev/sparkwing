package local

import (
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
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// handleHealth is the liveness probe. Returns 200 when the process is
// up; component failures land in problems[] so callers can surface them
// without a blanket outage banner. Only DB-unreachable flips to 503.
//
// Response: {"status": "ok" | "degraded", "problems": ["comp: detail"]}.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var problems []string

	// DB-unreachable is the only "return 503" trigger.
	if _, err := s.store.ListRuns(r.Context(), store.RunFilter{Limit: 1}); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
			"problems": []string{"db: " + err.Error()},
		})
		return
	}

	// Stuck-trigger canary: any trigger claimed > 30m with no /done.
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

	// Run success-rate canary over a 24h window. Only flag when total
	// is meaningful (>20) so a quiet cluster doesn't self-report
	// degraded from 0/0.
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

// --- Runs ---

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
	// Fetch pre-finish state for the pipeline + duration metric labels.
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
// shared with the cluster controller via store.ParseRunFilter.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	// Reconcile orphaned runs before reading so the dashboard never
	// shows a "running" row whose orchestrator process is dead. Same
	// sweep the CLI runs from runs status / runs list -- both
	// surfaces are kept in lockstep.
	_, _ = orchestrator.ReconcileOrphanedLocalRuns(r.Context(), s.store, 0)
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
	// Reconcile orphaned runs before reading -- see handleListRuns.
	_, _ = orchestrator.ReconcileOrphanedLocalRuns(r.Context(), s.store, 0)
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
		// Normalize nil node.deps to [] so the dashboard's iteration
		// over node.deps doesn't crash on JSON null.
		for _, n := range nodes {
			if n.Deps == nil {
				n.Deps = []string{}
			}
		}
		// Attach plan-snapshot-derived decorations (modifiers, groups,
		// approval, on_failure_of, inner-Work tree).
		decorated := api.DecorateNodes(nodes, run.PlanSnapshot)
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

// --- Nodes ---

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var body store.Node
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.RunID = runID // authoritative: path param wins over body
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

// --- Events ---

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

// --- Triggers ---

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
	Pipeline string                `json:"pipeline"`
	Args     map[string]string     `json:"args,omitempty"`
	Trigger  sparkwing.TriggerInfo `json:"trigger,omitempty"`
	Git      triggerReqGit         `json:"git,omitempty"`
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

type triggerResp struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
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

	// trigger_source is required so callers explicitly declare their
	// origin (a missing source would mis-route to the wrong worker).
	if body.Trigger.Source == "" {
		writeError(w, http.StatusBadRequest, errors.New("trigger.source is required"))
		return
	}
	runID := newRunID()

	// Cycle detection: walk the ancestor chain so a pipeline awaiting
	// itself fails fast with a "cycle: A -> B -> A" message instead of
	// deadlocking on a trigger that can never complete.
	if body.ParentRunID != "" {
		ancestors, err := s.store.GetRunAncestorPipelines(r.Context(), body.ParentRunID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("ancestor walk: %w", err))
			return
		}
		// Include the direct parent's pipeline as the first hop.
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
				// Format: "cycle: newest -> ... -> parent -> requested"
				trace := body.Pipeline
				for i := range chain {
					trace += " <- " + chain[i]
				}
				writeError(w, http.StatusConflict,
					fmt.Errorf("cycle: %s would re-enter itself (%s)", body.Pipeline, trace))
				return
			}
		}

		// Parent-repo inheritance for RunAndAwait:
		//   - Same-repo await (caller didn't set body.Git.Repo): copy
		//     parent's git context so the spawned run hits the same SHA.
		//   - Cross-repo await (caller set body.Git.Repo): do NOT copy
		//     parent's SHA -- it belongs to a different repo. The
		//     runner clones the caller's branch tip.
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

	// The trigger ID doubles as the eventual run ID.
	now := time.Now()
	if err := s.store.CreateTrigger(r.Context(), store.Trigger{
		ID:            runID,
		Pipeline:      body.Pipeline,
		Args:          body.Args,
		TriggerSource: body.Trigger.Source,
		TriggerUser:   body.Trigger.User,
		TriggerEnv:    body.Trigger.Env,
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

	// IMP-004: pre-allocate the Run row at trigger-intake so it shows
	// up in `runs list` even if the runner crashes or fails before
	// the orchestrator's CreateRun runs. See pkg/controller/handlers.go.
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
		Trigger:  body.Trigger,
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

// --- helpers ---

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
	// Upstream not finished -> 409. Callers should wait and retry.
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
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid seq: %v", err))
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
	// Metric label is pipeline-scoped; failure to resolve is
	// swallowed so a metric miss never turns a successful claim into
	// a 500.
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
	// Empty body is OK; it clears detail and still bumps the heartbeat.
	_ = decodeJSON(r, &body)
	if err := s.store.UpdateNodeActivity(r.Context(), runID, nodeID, body.Detail); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	// Only release_kind is honored; the audit identity comes from the
	// authenticated principal, not the client body.
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
