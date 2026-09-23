package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
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

	objectStore, objectStoreProblems := objectStoreHealth()
	problems = append(problems, objectStoreProblems...)

	egressState, egressProblems := s.egressHealth()
	problems = append(problems, egressProblems...)

	resp := map[string]any{
		"status": "ok", "auth": authState,
		"object_store": objectStore, "database": s.storageHealth(),
		"egress": egressState,
	}
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
		var authorized bool
		r, authorized = s.triggerClaimRequest(w, r, body.ID)
		if !authorized {
			return
		}
		if !s.bindRunRepoToTrigger(w, r, &body) {
			return
		}
	}
	// safety: the per-principal guards measure the authenticated caller, so the
	// principal comes from the token rather than anything the body asserts.
	runCtx := store.WithCreatingPrincipal(r.Context(), claimIdentity(r).Principal)
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if err := tenant.CreateRun(runCtx, body); err != nil {
		if errors.Is(err, store.ErrIDOwnedByAnotherTeam) {
			writeError(w, http.StatusConflict, err)
			return
		}
		if errors.Is(err, store.ErrSecretInputHash) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if s.writeComputeLimitRefusal(w, r, "", "", err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// safety: a run's declared repository is display metadata, so it is copied from
// the trigger rather than taken from the body, which keeps the two rows agreeing
// on one story. Nothing is granted on either value.
func (s *Server) bindRunRepoToTrigger(w http.ResponseWriter, r *http.Request, run *store.Run) bool {
	trig, err := s.store.GetTrigger(r.Context(), run.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if trig == nil {
		run.DeclaredRepo, run.RepoURL, run.GithubOwner, run.GithubRepo = "", "", "", ""
		return true
	}
	if run.DeclaredRepo != "" && run.DeclaredRepo != trig.Repo {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("repo %q does not match the trigger's repository %q", run.DeclaredRepo, trig.Repo))
		return false
	}
	run.DeclaredRepo, run.RepoURL = trig.Repo, trig.RepoURL
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	run, runErr := tenant.GetRun(r.Context(), runID)
	pipeline := ""
	if runErr == nil && run != nil {
		pipeline = run.Pipeline
	}
	otelutil.StampSpan(r.Context(), otelutil.SpanAttrs{
		RunID: runID, Pipeline: pipeline, Outcome: body.Status,
	})
	if err := tenant.FinishRun(r.Context(), runID, body.Status, body.Error); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	follow, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), finishRunFollowUpTimeout)
	defer cancel()
	if runErr == nil && run != nil {
		observeRunFinish(run.Pipeline, body.Status, time.Since(run.StartedAt))
		refreshed, rerr := tenant.GetRun(follow, runID)
		if rerr == nil {
			s.foldRunProfiles(follow, tenant, refreshed)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if err := tenant.UpdatePlanSnapshot(r.Context(), runID, snapshot); err != nil {
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runs, err := tenant.ListRuns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runs = store.RedactedRuns(runs)
	if runs == nil {
		runs = []*store.Run{}
	}
	w.Header().Set("X-Sparkwing-Run-Filter-Version", store.RunFilterVersion)
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

func nodeForResponse(node *store.Node) *store.Node {
	return api.PublicNode(node)
}

func nodesForResponse(nodes []*store.Node) []*store.Node {
	return api.PublicNodes(nodes)
}

func nodeForClaimResponse(node *store.Node) *store.Node {
	out := nodeForResponse(node)
	if out == nil {
		return nil
	}
	out.ClaimedBy = node.ClaimedBy
	out.ClaimGeneration = node.ClaimGeneration
	out.ClaimMembershipID = node.ClaimMembershipID
	out.ReservationID = node.ReservationID
	return out
}

type executorClaimPreparationResp struct {
	Summary       executorSchedulingSummaryResp `json:"summary"`
	Membership    executorMembershipResp        `json:"membership"`
	OfferDeadline *time.Time                    `json:"offer_deadline,omitempty"`
	Binding       json.RawMessage               `json:"execution_binding,omitempty"`
}

type executorSchedulingSummaryResp struct {
	RunID          string                 `json:"run_id"`
	NodeID         string                 `json:"node_id"`
	Resources      store.ExecutorResource `json:"resources"`
	ResourceDigest string                 `json:"resource_digest"`
	Slots          int                    `json:"slots"`
}

type executorMembershipResp struct {
	MembershipID      string `json:"membership_id"`
	WorkerID          string `json:"worker_id"`
	Eligible          bool   `json:"eligible"`
	EffectivePriority int    `json:"effective_priority"`
	MaxConcurrent     int    `json:"max_concurrent"`
}

func executorClaimPreparationForResponse(preparation *store.ExecutorClaimPreparation, binding executionpolicy.ClaimBinding) *executorClaimPreparationResp {
	if preparation == nil {
		return nil
	}
	encodedBinding, _ := executionpolicy.EncodeClaimBinding(binding)
	return &executorClaimPreparationResp{
		Summary: executorSchedulingSummaryResp{
			RunID: preparation.Summary.RunID, NodeID: preparation.Summary.NodeID,
			Resources: preparation.Summary.Resources, ResourceDigest: preparation.Summary.ResourceDigest,
			Slots: preparation.Summary.Slots,
		},
		Membership: executorMembershipResp{
			MembershipID: preparation.Membership.MembershipID, WorkerID: preparation.Membership.WorkerID,
			Eligible: preparation.Membership.Eligible, EffectivePriority: preparation.Membership.EffectivePriority,
			MaxConcurrent: preparation.Membership.MaxConcurrent,
		},
		OfferDeadline: preparation.OfferDeadline,
		Binding:       encodedBinding,
	}
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	run, err := tenant.GetRun(r.Context(), runID)
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
		nodes = nodesForResponse(nodes)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	run, err := tenant.GetLatestRun(r.Context(), name, statuses, maxAge)
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
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodesForResponse(nodes)})
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
	if s.holdsLiveTriggerClaim(r.Context(), runID) {
		if run, err := s.store.GetRun(r.Context(), runID); err == nil && run.FinishedAt != nil {
			writeError(w, http.StatusConflict, finishedRunConflict(runID, run, body.NodeID))
			return
		}
	}
	if err := s.store.CreateNode(r.Context(), body); err != nil {
		if s.writeComputeLimitRefusal(w, r, runID, body.NodeID, err) {
			return
		}
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// safety: a fence header the caller merely shaped correctly proves nothing, so
// the run's recorded status and error go only to a caller whose trigger claim
// the store would honor on the write itself.
func (s *Server) holdsLiveTriggerClaim(ctx context.Context, runID string) bool {
	fence, fenced := store.TriggerClaimFenceFromContext(ctx)
	if !fenced {
		return true
	}
	live, err := s.store.TriggerClaimFenceIsLive(ctx, runID, fence.Claimant, fence.ClaimGeneration, time.Now())
	return err == nil && live
}

// safety: a run the reaper or an operator already ended cannot take new work,
// and a child told so reports why instead of reading a server fault.
func finishedRunConflict(runID string, run *store.Run, nodeID string) error {
	detail := fmt.Sprintf("run %s finished as %s before node %s was created",
		runID, run.Status, nodeID)
	if run.Error != "" {
		detail += ": " + run.Error
	}
	return errors.New(detail)
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
	s.settleFinishedNode(r, runID, nodeID)
	s.liveLogs.Finish(runID, nodeID)
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
	p, authenticated := PrincipalFromContext(r.Context())
	if authenticated && !p.HasScope(ScopeAdmin) {
		if body.NodeID == "" {
			var authorized bool
			r, authorized = s.triggerClaimRequest(w, r, runID)
			if !authorized {
				return
			}
		} else if fence, err := nodeClaimFenceFromRequest(r); err == nil {
			r = r.WithContext(store.WithNodeClaimFence(r.Context(), fence))
		} else {
			generation, parseErr := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
			if parseErr != nil || generation < 1 {
				writeAuthError(w, http.StatusForbidden, authErrorBody{
					Code: "claim_required", Principal: p.label(),
					Message: "node " + runID + "/" + body.NodeID + " requires its exact claim fence",
				})
				return
			}
			r = r.WithContext(store.WithTriggerClaimFence(r.Context(), store.TriggerClaimFence{
				Claimant: claimIdentity(r), ClaimGeneration: generation,
			}))
		}
	}
	seq, err := s.store.AppendEventCharged(r.Context(), chargedPrincipal(r),
		runID, body.NodeID, body.Kind, body.Payload)
	if writeStorageWriteRefusal(w, s.logger, err) {
		return
	}
	if errors.Is(err, store.ErrLockHeld) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, appendEventResp{Seq: seq})
}

// safety: the body names no user; the run is attributed to the credential
// that submitted it, so a caller cannot put a run under someone else's name.
type triggerReqMeta struct {
	Source string            `json:"source,omitempty"`
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
	"GITHUB_REPOSITORY":             true,
	sparkwing.EnvGitHubEventName:    true,
	sparkwing.EnvPRNumber:           true,
	sparkwing.EnvPRAction:           true,
	sparkwing.EnvPRBaseRef:          true,
	sparkwing.EnvPRBaseSHA:          true,
	sparkwing.EnvPRHeadRef:          true,
	sparkwing.EnvPRHeadSHA:          true,
	"SPARKWING_START_AT":            true,
	"SPARKWING_STOP_AT":             true,
	"SPARKWING_ONLY":                true,
	"SPARKWING_DRY_RUN":             true,
	"SPARKWING_NO_CACHE":            true,
	bincache.WorkspaceBaseRefEnvKey: true,
	bincache.WorkspaceBaseSHAEnvKey: true,
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

// safety: the commit-status reporter spends the controller's GitHub token on
// whatever these name, and only a delivery signed by an operator's binding is
// trusted to name them.
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

// submitterName is the authenticated principal a trigger is attributed
// to, or empty on a controller that runs without authentication.
func submitterName(r *http.Request) string {
	if p, ok := PrincipalFromContext(r.Context()); ok {
		return p.Name
	}
	return ""
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
	if !s.authorizeTriggerParent(w, r, body.ParentRunID, body.ParentNodeID) {
		return
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if body.RetryOf != "" {
		// safety: retry_of joins the new run to the named run's attempt tree,
		// which the attempts route serves whole, so it has to name the
		// caller's own run; another team's run answers as no run.
		if _, err := tenant.GetRun(r.Context(), body.RetryOf); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, runNotFound(body.RetryOf))
				return
			}
			s.writeInternalError(w, r, "retry_of lookup", err)
			return
		}
	}

	runID := newRunID()
	repoInherited := body.ParentRunID != "" && body.Git.Repo == ""

	if body.ParentRunID != "" {
		ancestors, err := s.store.GetRunAncestorPipelines(r.Context(), body.ParentRunID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("ancestor walk: %w", err))
			return
		}
		parent, perr := tenant.GetRun(r.Context(), body.ParentRunID)
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
			body.Git.Repo = parent.DeclaredRepo
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

	// safety: a trigger creates the run it names, so the hourly guard measures
	// the principal that triggered it exactly as a direct create does.
	triggerCtx := store.WithCreatingPrincipal(r.Context(), claimIdentity(r).Principal)
	intake := triggerIntake{
		RunID:         runID,
		Pipeline:      body.Pipeline,
		Args:          body.Args,
		Source:        body.Trigger.Source,
		User:          submitterName(r),
		Env:           sanitizeTriggerEnv(body.Trigger.Env),
		Git:           body.Git,
		ParentRunID:   body.ParentRunID,
		ParentNodeID:  body.ParentNodeID,
		RetryOf:       body.RetryOf,
		RepoInherited: repoInherited,
		At:            time.Now(),
	}

	principal := s.floodKey(r, "pipeline:"+body.Pipeline)
	// safety: a redelivery is answered with the run it already started before
	// anything is charged, so repeating one never spends the submitter's cap.
	release, original, duplicate := s.claimSubmissionDigest(principal, intake)
	if duplicate {
		writeJSON(w, http.StatusConflict, triggerResp{RunID: original, Status: "duplicate"})
		return
	}
	if !s.admitTriggerSubmission(w, r, principal, body.Trigger.Source) {
		release()
		return
	}

	if err := s.admitTrigger(triggerCtx, tenant, intake); err != nil {
		release()
		if s.writeComputeLimitRefusal(w, r, "", "", err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusAccepted, triggerResp{
		RunID:  runID,
		Status: "dispatched",
	})
}

// safety: the digest is reserved before the run is created, so two simultaneous
// identical submissions cannot both start one; the release undoes a reservation no run followed.
func (s *Server) claimSubmissionDigest(principal string, in triggerIntake) (release func(), original string, duplicate bool) {
	if s.flood == nil || s.flood.dedupe == nil {
		return func() {}, "", false
	}
	digest := submissionDigest(principal, in)
	if original, found := s.flood.dedupe.claim(digest, in.RunID, in.At); found {
		s.logger.Warn("trigger deduplicated",
			"principal", principal, "pipeline", in.Pipeline,
			"reason", "an identical submission is already inside the dedupe window",
			"run_id", original)
		return func() {}, original, true
	}
	return func() { s.flood.dedupe.forget(digest) }, "", false
}

// safety: Env arrives sanitized; a caller inside the process supplies only keys
// a run may read, because nothing filters it again here.
type triggerIntake struct {
	RunID         string
	Pipeline      string
	Args          map[string]string
	Source        string
	User          string
	Env           map[string]string
	Git           triggerReqGit
	ParentRunID   string
	ParentNodeID  string
	RetryOf       string
	RepoInherited bool
	// safety: a second intake under one key fails with
	// store.ErrDuplicateIdempotencyKey rather than starting a second run.
	IdempotencyKey string
	At             time.Time
}

// safety: every path that starts a run on this controller writes its three rows
// here -- the trigger, the pending run, the dispatch -- so an HTTP submission
// and a schedule the controller fired land identically.
func (s *Server) admitTrigger(ctx context.Context, t *store.Tenant, in triggerIntake) error {
	// safety: the trigger and the run it names are written together, so a guard
	// that refuses the run leaves no trigger behind for a worker to claim.
	if err := t.CreateTriggerWithRun(ctx, store.Trigger{
		ID:             in.RunID,
		Pipeline:       in.Pipeline,
		Args:           in.Args,
		TriggerSource:  in.Source,
		TriggerUser:    in.User,
		TriggerEnv:     in.Env,
		GitBranch:      in.Git.Branch,
		GitSHA:         in.Git.SHA,
		Repo:           in.Git.Repo,
		RepoURL:        in.Git.RepoURL,
		GithubOwner:    in.Git.GithubOwner,
		GithubRepo:     in.Git.GithubRepo,
		CreatedAt:      in.At,
		ParentRunID:    in.ParentRunID,
		ParentNodeID:   in.ParentNodeID,
		RetryOf:        in.RetryOf,
		RepoInherited:  in.RepoInherited,
		IdempotencyKey: in.IdempotencyKey,
	}, store.Run{
		ID:            in.RunID,
		Pipeline:      in.Pipeline,
		Status:        "pending",
		TriggerSource: in.Source,
		GitBranch:     in.Git.Branch,
		GitSHA:        in.Git.SHA,
		Args:          in.Args,
		ParentRunID:   in.ParentRunID,
		DeclaredRepo:  in.Git.Repo,
		RepoURL:       in.Git.RepoURL,
		GithubOwner:   in.Git.GithubOwner,
		GithubRepo:    in.Git.GithubRepo,
		RetryOf:       in.RetryOf,
		CreatedAt:     in.At,
		StartedAt:     in.At,
	}); err != nil {
		if errors.Is(err, store.ErrDuplicateIdempotencyKey) || errors.Is(err, store.ErrComputeLimit) {
			return err
		}
		return fmt.Errorf("persist the trigger and its run: %w", err)
	}

	s.recordQueueActivity(in.At)

	return s.dispatcher.Dispatch(ctx, RunRequest{
		RunID:    in.RunID,
		Pipeline: in.Pipeline,
		Args:     in.Args,
		Trigger: sparkwing.TriggerInfo{
			Source: in.Source,
			User:   in.User,
		},
		Git: &sparkwing.Git{
			Branch:  in.Git.Branch,
			SHA:     in.Git.SHA,
			Repo:    in.Git.Repo,
			RepoURL: in.Git.RepoURL,
		},
		ParentRunID: in.ParentRunID,
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
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
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
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	trigs, err := tenant.ListTriggers(r.Context(), filter)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	// safety: the parent is named in the query, outside the path the team
	// boundary reads, so it is proven in the caller's team here; another team's
	// parent answers as having spawned nothing.
	owned, err := tenant.OwnsRun(r.Context(), parentRunID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !owned {
		writeJSON(w, http.StatusOK, map[string]string{"run_id": ""})
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
	if writeClaimTeamRefusal(w, err) {
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeInternalError(w, r, "claim trigger", err)
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
	if writeClaimTeamRefusal(w, err) {
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeClaimPollAdvice(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.writeInternalError(w, r, "claim next trigger", err)
		return
	}
	s.recordQueueActivity(time.Now())
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
	if errors.Is(err, store.ErrLockHeld) {
		status = http.StatusConflict
	}
	message := err.Error()
	if status >= http.StatusInternalServerError {
		message = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) writeInternalError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	s.logger.Error(operation, "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, err)
}

type executionAdmissionErrorBody struct {
	Error          string          `json:"error"`
	Code           string          `json:"code"`
	RunID          string          `json:"run_id,omitempty"`
	NodeID         string          `json:"node_id,omitempty"`
	Scope          string          `json:"scope,omitempty"`
	Missing        []string        `json:"missing,omitempty"`
	MinimumRelease string          `json:"minimum_release,omitempty"`
	SafeHold       bool            `json:"safe_hold"`
	PolicyProtocol int             `json:"policy_protocol,omitempty"`
	HelperMinimum  int             `json:"helper_minimum,omitempty"`
	HelperMaximum  int             `json:"helper_maximum,omitempty"`
	Binding        json.RawMessage `json:"execution_binding,omitempty"`
}

func writeExecutionAdmissionError(w http.ResponseWriter, err error, bindings ...executionpolicy.ClaimBinding) bool {
	var upgrade *executionpolicy.UpgradeRequiredError
	if errors.As(err, &upgrade) {
		writeJSON(w, http.StatusConflict, executionAdmissionErrorBody{
			Error: err.Error(), Code: "upgrade_required", Scope: upgrade.Scope,
			Missing: append([]string(nil), upgrade.Missing...), MinimumRelease: upgrade.MinimumRelease,
			SafeHold: upgrade.SafeHold,
		})
		return true
	}
	var protocol *executionpolicy.ProtocolIncompatibleError
	if errors.As(err, &protocol) {
		writeJSON(w, http.StatusConflict, executionAdmissionErrorBody{
			Error: err.Error(), Code: "protocol_incompatible", PolicyProtocol: protocol.PolicyProtocol,
			HelperMinimum: protocol.HelperMinimum, HelperMaximum: protocol.HelperMaximum,
		})
		return true
	}
	if errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		body := executionAdmissionErrorBody{Error: err.Error(), Code: "body_attestation_required"}
		var detail *executionpolicy.BodyAttestationRequiredError
		if errors.As(err, &detail) {
			body.RunID, body.NodeID = detail.RunID, detail.NodeID
		}
		if len(bindings) != 0 {
			body.Binding, _ = executionpolicy.EncodeClaimBinding(bindings[0])
		}
		writeJSON(w, http.StatusConflict, body)
		return true
	}
	return false
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
	writeJSON(w, http.StatusOK, nodeForResponse(n))
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
	HolderID       string          `json:"holder_id"`
	RunID          string          `json:"run_id,omitempty"`
	NodeID         string          `json:"node_id,omitempty"`
	ExecutorName   string          `json:"executor_name,omitempty"`
	ReservationID  string          `json:"reservation_id,omitempty"`
	ResourceDigest string          `json:"resource_digest,omitempty"`
	Slot           int             `json:"slot,omitempty"`
	LeaseSecs      int             `json:"lease_secs,omitempty"`
	Labels         []string        `json:"labels,omitempty"`
	Headroom       *claimHeadroom  `json:"headroom,omitempty"`
	Capacity       *claimCapacity  `json:"capacity,omitempty"`
	Binding        json.RawMessage `json:"execution_binding,omitempty"`
}

type claimHeadroom struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
	QueueDepth  int     `json:"queue_depth"`
}

type claimCapacity struct {
	MaxConcurrent int `json:"max_concurrent"`
	ActiveClaims  int `json:"active_claims"`
}

var errAssistedOfferRequired = errors.New("credential is enrolled; assisted offer protocol is required")

func (s *Server) rejectEnrolledLegacyClaim(r *http.Request) error {
	claimant := claimIdentity(r)
	if claimant.TokenPrefix == "" {
		return nil
	}
	_, err := s.store.ExecutorNameForTokenPrefix(r.Context(), claimant.TokenPrefix)
	switch {
	case err == nil:
		return errAssistedOfferRequired
	case errors.Is(err, store.ErrNotFound):
		return nil
	default:
		return err
	}
}

func (s *Server) recordAdvertisedHeadroom(r *http.Request, holderID string, h *claimHeadroom) {
	if h == nil {
		return
	}
	team := store.DefaultTeam
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil && store.NormalizeTeam(p.Team) != "" {
		team = store.NormalizeTeam(p.Team)
	}
	name, _ := holderName(holderID)
	s.runnerHeadroom.record(name, runnerHeadroom{
		Team:        team,
		Cores:       h.Cores,
		MemoryBytes: h.MemoryBytes,
		QueueDepth:  h.QueueDepth,
		UpdatedAt:   time.Now(),
	})
}

func (s *Server) placementContext(ctx context.Context, claimer presenceKey) context.Context {
	if s.placement.hold <= 0 {
		return ctx
	}
	return store.WithClaimPlacement(ctx, store.ClaimPlacement{
		DefaultPrefers: s.placement.defaultPrefers,
		Hold:           s.placement.hold,
		Live:           s.runnerPresence.live(time.Now(), s.placement.liveness, claimer),
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
	lease := time.Duration(body.LeaseSecs) * time.Second
	if body.ExecutorName != "" {
		if body.RunID == "" || body.NodeID == "" || body.ReservationID == "" || body.ResourceDigest == "" || body.Slot < 0 {
			writeError(w, http.StatusBadRequest, errors.New("run_id, node_id, reservation_id, resource_digest, and non-negative slot are required for an executor offer"))
			return
		}
		if s.assistedRunID != "" && body.RunID != s.assistedRunID {
			writeError(w, http.StatusForbidden, errors.New("executor offer does not belong to this foreground run"))
			return
		}
		ctx := r.Context()
		binding, err := executionpolicy.DecodeClaimBinding(body.Binding)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !binding.IsZero() {
			ctx, err = executionpolicy.WithOfferBinding(ctx, binding)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		result, err := s.store.OfferExecutorClaim(ctx, claimIdentity(r), store.ExecutorClaimOffer{
			ExecutorName: body.ExecutorName, HolderID: body.HolderID,
			RunID: body.RunID, NodeID: body.NodeID, ReservationID: body.ReservationID,
			ResourceDigest: body.ResourceDigest, Slot: body.Slot, Lease: lease,
		})
		if writeClaimTeamRefusal(w, err) {
			return
		}
		if err != nil {
			if s.writeCreditsRefusal(w, r, err) {
				return
			}
			if s.writeUnpricedClassRefusal(w, r, err) {
				return
			}
			if s.writeComputeLimitRefusal(w, r, body.RunID, body.NodeID, err) {
				return
			}
			if writeExecutionAdmissionError(w, err) {
				return
			}
			if errors.Is(err, store.ErrExecutorCredentialMismatch) {
				writeError(w, http.StatusForbidden, store.ErrExecutorCredentialMismatch)
				return
			}
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLockHeld) {
				w.Header().Set("X-Sparkwing-Claim-Offer-State", "empty")
				s.writeClaimPollAdvice(w)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			s.writeInternalError(w, r, "claim node", err)
			return
		}
		if result.Node == nil {
			if result.Pending {
				w.Header().Set("X-Sparkwing-Claim-Offer-State", "pending")
			} else {
				w.Header().Set("X-Sparkwing-Claim-Offer-State", "empty")
				s.writeClaimPollAdvice(w)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeClaimedNode(w, r, s, result.Node)
		return
	}
	if err := s.rejectEnrolledLegacyClaim(r); err != nil {
		if errors.Is(err, errAssistedOfferRequired) {
			writeError(w, http.StatusForbidden, err)
			return
		}
		s.writeInternalError(w, r, "claim node", err)
		return
	}
	if body.Capacity != nil && (body.Capacity.MaxConcurrent < 0 || body.Capacity.ActiveClaims < 0) {
		writeError(w, http.StatusBadRequest, errors.New("capacity must carry non-negative max_concurrent and active_claims"))
		return
	}
	s.recordAdvertisedHeadroom(r, body.HolderID, body.Headroom)
	claimer := presenceKey{tokenPrefix: claimIdentity(r).TokenPrefix, name: presenceName(body.HolderID)}
	s.runnerPresence.record(claimer, body.Labels, body.Capacity, time.Now())
	n, err := s.store.ClaimNextReadyNode(s.placementContext(r.Context(), claimer),
		claimIdentity(r), body.HolderID, lease, body.Labels)
	if writeClaimTeamRefusal(w, err) {
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeClaimPollAdvice(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if s.writeCreditsRefusal(w, r, err) {
			return
		}
		if s.writeUnpricedClassRefusal(w, r, err) {
			return
		}
		if s.writeClaimComputeLimitRefusal(w, r, err) {
			return
		}
		s.writeInternalError(w, r, "claim node", err)
		return
	}
	s.runnerPresence.awarded(claimer)
	s.noteRunnerAlarm(r)
	writeClaimedNode(w, r, s, n)
}

type claimNamedNodeReq struct {
	HolderID  string `json:"holder_id"`
	LeaseSecs int    `json:"lease_secs"`
	// safety: without this the caller runs the node on an executor it already
	// has, so a metered claim is held to the class the warm pool serves.
	SizesToClass bool `json:"sizes_to_class,omitempty"`
}

func (s *Server) handleClaimNamedNode(w http.ResponseWriter, r *http.Request) {
	var body claimNamedNodeReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.HolderID == "" {
		writeError(w, http.StatusBadRequest, errors.New("holder_id is required"))
		return
	}
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	if !s.mayClaimNamedNode(w, r, runID, nodeID) {
		return
	}
	n, err := s.store.ClaimNamedNode(r.Context(), claimIdentity(r), runID, nodeID,
		body.HolderID, time.Duration(body.LeaseSecs)*time.Second,
		store.NamedClaimOptions{SizesToClass: body.SizesToClass})
	if writeClaimTeamRefusal(w, err) {
		return
	}
	// safety: the flag is what admits a class the queue would refuse, and only
	// the operator's own pool token may set it, so every use is on the record.
	if body.SizesToClass && err == nil {
		slog.InfoContext(r.Context(), "a named claim took a node at its cpu class",
			"run_id", runID, "node_id", nodeID, "holder_id", body.HolderID,
			"cpu_class_cores", n.CreditCPUClassCores)
	}
	if err != nil {
		if s.writeCreditsRefusal(w, r, err) {
			return
		}
		if s.writeUnpricedClassRefusal(w, r, err) {
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		s.writeInternalError(w, r, "claim named node", err)
		return
	}
	writeClaimedNode(w, r, s, n)
}

// safety: naming a node skips the queue, and every pipeline pod carries a
// claim-scoped token, so an unlabeled node the queue already opened is fair
// game and anything else needs the run's dispatch claim. A named claim
// advertises no labels, so nothing else could honor a requirement.
func (s *Server) mayClaimNamedNode(w http.ResponseWriter, r *http.Request, runID, nodeID string) bool {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.HasScope(ScopeAdmin) {
		return true
	}
	n, err := s.store.GetNode(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return false
		}
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if n.ReadyAt != nil && len(n.NeedsLabels) == 0 {
		return true
	}
	held, err := s.store.PrincipalHoldsTriggerClaim(r.Context(), runID, claimIdentity(r), time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if held {
		return true
	}
	reason := "has not been made ready"
	if len(n.NeedsLabels) > 0 {
		reason = "requires runner labels a named claim cannot advertise"
	}
	writeAuthError(w, http.StatusForbidden, authErrorBody{
		Code: "claim_required", Principal: p.label(),
		Message: "node " + runID + "/" + nodeID + " " + reason + ", so naming it requires the run's live trigger claim",
	})
	return false
}

func writeClaimedNode(w http.ResponseWriter, r *http.Request, s *Server, n *store.Node) {
	s.recordQueueActivity(time.Now())
	pipeline := ""
	if run, err := s.store.GetRun(r.Context(), n.RunID); err == nil && run != nil {
		pipeline = run.Pipeline
	}
	observeNodeClaim(pipeline)
	observeClaimWait(n)
	otelutil.StampSpan(r.Context(), otelutil.SpanAttrs{
		RunID: n.RunID, NodeID: n.NodeID, Pipeline: pipeline,
	})
	writeJSON(w, http.StatusOK, nodeForClaimResponse(n))
}

// safety: only a host's own daemon can vouch for a node run in the caller's
// process, so every other controller refuses the shape outright rather than
// letting a runs.write caller invent one.
var errLocalExecutionUnsupported = errors.New(
	"local execution attempts are accepted only by a host's own admission daemon")

func (s *Server) handleAcknowledgeNodeExecutionStart(w http.ResponseWriter, r *http.Request) {
	var body store.ExecutionStart
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	_, triggerClaim := store.TriggerClaimFenceFromContext(r.Context())
	local := body.ExecutorKind == store.ExecutorKindLocal
	if local && !s.localExecution {
		writeError(w, http.StatusBadRequest, errLocalExecutionUnsupported)
		return
	}
	if body.AttemptOrdinal < 1 || (!triggerClaim && !local && (body.HolderID == "" || body.ClaimGeneration < 1)) {
		writeError(w, http.StatusBadRequest, errors.New("attempt_ordinal and an exact execution identity are required"))
		return
	}
	if local && body.ExecutorID == "" {
		writeError(w, http.StatusBadRequest, errors.New("a local execution attempt requires executor_id"))
		return
	}
	err := s.store.AcknowledgeNodeExecutionStart(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), claimIdentity(r), body)
	if errors.Is(err, store.ErrLockHeld) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFinishNodeExecutionAttempt(w http.ResponseWriter, r *http.Request) {
	var body store.ExecutionAttemptFinish
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	_, triggerClaim := store.TriggerClaimFenceFromContext(r.Context())
	local := body.ExecutorKind == store.ExecutorKindLocal
	if local && !s.localExecution {
		writeError(w, http.StatusBadRequest, errLocalExecutionUnsupported)
		return
	}
	if body.AttemptOrdinal < 1 || body.Outcome == "" || (!triggerClaim && !local && (body.HolderID == "" || body.ClaimGeneration < 1)) {
		writeError(w, http.StatusBadRequest, errors.New("attempt_ordinal, outcome, and an exact execution identity are required"))
		return
	}
	if !validExecutionAttemptResult(body.Outcome, body.FailureReason) {
		writeError(w, http.StatusBadRequest, errors.New("invalid execution-attempt outcome or failure_reason"))
		return
	}
	err := s.store.FinishNodeExecutionAttempt(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), claimIdentity(r), body)
	if errors.Is(err, store.ErrLockHeld) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.finalizeMeteredNode(r, r.PathValue("id"), r.PathValue("nodeID"))
	w.WriteHeader(http.StatusNoContent)
}

func validExecutionAttemptResult(outcome, reason string) bool {
	if outcome == "success" || outcome == "cancelled" {
		return reason == ""
	}
	if outcome != "failed" {
		return false
	}
	switch reason {
	case store.FailureUnknown, store.FailureOOMKilled, store.FailureTimeout,
		store.FailureNoProgressTimeout, store.FailureVerify, store.FailureQueueTimeout,
		store.FailureLogsAuth, store.FailureLogsDropped:
		return true
	default:
		return false
	}
}

// safety: an append is authorized by the claim the writer still holds, not by
// its execution attempt still being open, because a node's closing lines land
// after the executor closes the attempt.
func (s *Server) handleValidateNodeLogClaim(w http.ResponseWriter, r *http.Request) {
	hasNodeIdentity, hasTriggerIdentity := claimIdentityShape(r)
	if hasNodeIdentity && hasTriggerIdentity {
		writeError(w, http.StatusBadRequest, errors.New("node attempt and trigger identities cannot be combined"))
		return
	}
	if p, ok := PrincipalFromContext(r.Context()); ok && p.HasScope(ScopeAdmin) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var held bool
	var err error
	if hasNodeIdentity {
		fence, fenceErr := nodeClaimFenceFromRequest(r)
		if fenceErr != nil {
			writeError(w, http.StatusConflict, store.ErrLockHeld)
			return
		}
		ordinal, parseErr := strconv.Atoi(r.Header.Get(store.AttemptOrdinalHeader))
		if parseErr == nil && ordinal > 0 {
			held, err = s.store.NodeExecutionAttemptBelongsToLiveClaim(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), fence, ordinal, time.Now())
		}
	} else if hasTriggerIdentity {
		generation, parseErr := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
		if parseErr != nil || generation < 1 {
			writeError(w, http.StatusConflict, store.ErrLockHeld)
			return
		}
		fence := store.TriggerClaimFence{Claimant: claimIdentity(r), ClaimGeneration: generation}
		rawOrdinal := r.Header.Get(store.AttemptOrdinalHeader)
		if rawOrdinal == "" && r.PathValue("nodeID") == "_compile" {
			held, err = s.store.TriggerClaimFenceIsLive(r.Context(), r.PathValue("id"), claimIdentity(r), generation, time.Now())
		} else {
			ordinal, ordinalErr := strconv.Atoi(rawOrdinal)
			if ordinalErr == nil && ordinal > 0 {
				held, err = s.store.TriggerExecutionAttemptBelongsToLiveClaim(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), fence, ordinal, time.Now())
			}
		}
	} else {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !held {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePrepareNodeClaim(w http.ResponseWriter, r *http.Request) {
	var body claimNodeReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.ExecutorName == "" {
		writeError(w, http.StatusBadRequest, errors.New("executor_name is required"))
		return
	}
	var preparation *store.ExecutorClaimPreparation
	sink := executionpolicy.NewPreparationSink()
	ctx := executionpolicy.WithPreparationSink(r.Context(), sink)
	var err error
	if s.assistedRunID != "" {
		preparation, err = s.store.PrepareExecutorClaimForRun(ctx, claimIdentity(r), body.ExecutorName, s.assistedRunID)
	} else {
		preparation, err = s.store.PrepareNextExecutorClaim(ctx, claimIdentity(r), body.ExecutorName)
	}
	if writeClaimTeamRefusal(w, err) {
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		if writeExecutionAdmissionError(w, err, sink.Load()) {
			return
		}
		if errors.Is(err, store.ErrExecutorCredentialMismatch) {
			writeError(w, http.StatusForbidden, store.ErrExecutorCredentialMismatch)
			return
		}
		s.writeInternalError(w, r, "prepare node claim", err)
		return
	}
	writeJSON(w, http.StatusOK, executorClaimPreparationForResponse(preparation, sink.Load()))
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

func (s *Server) handleResetNodeForAutoRetry(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ResetNodeForAutoRetry(r.Context(), r.PathValue("id"), r.PathValue("nodeID")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFinalizeNodeReady(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	result, err := s.store.FinalizeExecutorClaimRound(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeInternalError(w, r, "finalize ready node", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Revoked bool `json:"revoked"`
		Pending bool `json:"pending,omitempty"`
	}{Revoked: result.Revoked, Pending: result.Pending})
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
	fence, err := nodeClaimFenceFromRequest(r)
	if err != nil || fence.HolderID != body.HolderID {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return
	}
	s.recordAdvertisedHeadroom(r, body.HolderID, body.Headroom)
	lease := time.Duration(body.LeaseSecs) * time.Second
	claimCtx := store.WithNodeClaimFence(r.Context(), fence)
	if err := s.store.HeartbeatNodeClaim(claimCtx, runID, nodeID, fence.Claimant, body.HolderID, lease); err != nil {
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// safety: the runner abandons a node whose claim the controller refuses,
	// which is how a cancellation for an empty balance reaches it.
	if s.chargeMeteredHeartbeat(r, runID, nodeID) || s.stopForWallClockLimit(r, runID, nodeID) {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
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
	if err := s.store.SetNodeArtifactManifestCharged(r.Context(), chargedPrincipal(r),
		runID, nodeID, body.ManifestDigest); err != nil {
		if writeStorageWriteRefusal(w, s.logger, err) {
			return
		}
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
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
	if err := store.ValidateStepTerminalStatus(body.Status); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if _, err := tenant.GetRun(r.Context(), runID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tenant.TouchRunHeartbeat(r.Context(), runID); err != nil {
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
	writeJSON(w, http.StatusOK, api.PublicEvents(events))
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
