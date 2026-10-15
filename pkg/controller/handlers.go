package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var problems []string

	authState := "disabled"
	if s.auth != nil {
		authState = "enabled"
	}

	if _, err := s.store.ListRuns(r.Context(), store.RunFilter{Limit: 1}); err != nil {
		s.logger.Error("controller health database check failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
			"auth":     authState,
			"problems": []string{"db: unavailable"},
		})
		return
	}

	objectStore, objectStoreProblems := objectStoreHealth(s.bucketMeasured())
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

func nodeForResponse(node *store.Node) *store.Node {
	return api.PublicNode(node)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
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
		if run, err := tenant.GetRun(r.Context(), runID); err == nil && run.FinishedAt != nil {
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	if err := tenant.StartNode(r.Context(), runID, nodeID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type finishNodeReq struct {
	Outcome       string           `json:"outcome"`
	Error         string           `json:"error,omitempty"`
	Output        *store.OutputRef `json:"output,omitempty"`
	FailureReason string           `json:"failure_reason,omitempty"`
	ExitCode      *int             `json:"exit_code,omitempty"`
}

func (s *Server) handleFinishNode(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
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
	if err := tenant.FinishNodeWithOutputRef(r.Context(), runID, nodeID, body.Outcome, body.Error, body.Output, body.FailureReason, body.ExitCode); err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body updateDepsReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := tenant.UpdateNodeDeps(r.Context(), runID, nodeID, body.Deps); err != nil {
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
	if err := store.ValidateEventKind(body.Kind); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	seq, err := tenant.AppendEventCharged(r.Context(), s.leaseHolder,
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

// safety: the run is attributed to the credential that submitted it, so a
// caller cannot put a run under someone else's name. User is still decoded
// because released CLIs send it, and it is never read.
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
	Runner       string            `json:"runner,omitempty"`
	Reason       string            `json:"reason,omitempty"`
}

type triggerResp struct {
	RunID                string `json:"run_id"`
	Status               string `json:"status"`
	RunnerToken          string `json:"runner_token,omitempty"`
	RunnerTokenExpiresAt int64  `json:"runner_token_expires_at,omitempty"`
}

// safety: every other trigger_env key a run reads is controller-written, so an inbound copy forges it.
var submittedTriggerEnvKeys = map[string]bool{
	"GITHUB_REPOSITORY":               true,
	"GITHUB_REF":                      true,
	"GITHUB_REF_TYPE":                 true,
	"GITHUB_ACTION":                   true,
	"GITHUB_LABEL":                    true,
	"GITHUB_MERGED":                   true,
	"GITHUB_TAG_NAME":                 true,
	"GITHUB_TAG":                      true,
	"GITHUB_EVENT_NAME":               true,
	"GITHUB_PR_NUMBER":                true,
	"GITHUB_PR_ACTION":                true,
	"GITHUB_BASE_REF":                 true,
	"GITHUB_BASE_SHA":                 true,
	"GITHUB_HEAD_REF":                 true,
	"GITHUB_HEAD_SHA":                 true,
	"SPARKWING_START_AT":              true,
	"SPARKWING_STOP_AT":               true,
	"SPARKWING_ONLY":                  true,
	"SPARKWING_DRY_RUN":               true,
	"SPARKWING_NO_CACHE":              true,
	bincache.WorkspaceBaseRefEnvKey:   true,
	bincache.WorkspaceBaseSHAEnvKey:   true,
	bincache.SourceBundleObjectEnvKey: true,
}

var githubProvenanceEnvKeys = map[string]bool{
	"GITHUB_REF":        true,
	"GITHUB_REF_TYPE":   true,
	"GITHUB_ACTION":     true,
	"GITHUB_LABEL":      true,
	"GITHUB_MERGED":     true,
	"GITHUB_TAG_NAME":   true,
	"GITHUB_TAG":        true,
	"GITHUB_EVENT_NAME": true,
	"GITHUB_PR_NUMBER":  true,
	"GITHUB_PR_ACTION":  true,
	"GITHUB_BASE_REF":   true,
	"GITHUB_BASE_SHA":   true,
	"GITHUB_HEAD_REF":   true,
	"GITHUB_HEAD_SHA":   true,
}

var githubRepoSlug = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// safety: the git cache path builds its clone URL from this key, so it stays a
// slug and never becomes a URL; handleTrigger also holds it to the repository
// git.repo_url names, since a direct runner fetches that one.
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
	here, err := s.hereRunnerRequest(r, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	sourceKey := body.Trigger.Env[bincache.SourceBundleObjectEnvKey]
	workspaceSource := strings.HasPrefix(body.Trigger.Source, "pipeline-working-tree@")
	if workspaceSource {
		sourceDigest, valid := store.SourceKeyDigest(sourceKey)
		if body.Git.SHA == "" || !valid {
			writeError(w, http.StatusBadRequest, errors.New("working-tree trigger needs a source bundle key and snapshot commit"))
			return
		}
		obj, readErr := s.store.CommittedObject(r.Context(), tenant.Team(), sourceKey)
		if errors.Is(readErr, store.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, errors.New("source bundle is not committed for this team; upload it before triggering"))
			return
		}
		if readErr != nil {
			s.writeInternalError(w, r, "read committed source bundle", readErr)
			return
		}
		principal, _ := PrincipalFromContext(r.Context())
		if principal == nil || principal.Kind != store.TokenKindUser || obj.Principal != principal.Name ||
			obj.Kind != store.StorageCache || obj.Provenance != "local" || obj.SHA256 != sourceDigest {
			writeError(w, http.StatusForbidden, errors.New("source bundle is not owned by this submitter"))
			return
		}
	} else if sourceKey != "" {
		writeError(w, http.StatusBadRequest, errors.New("source bundles require a working-tree trigger"))
		return
	}
	if here != nil && sourceKey != "" {
		writeError(w, http.StatusBadRequest, errors.New("a runner here run builds the caller's checkout, so it takes no source bundle"))
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
		parent, perr := tenant.GetRun(r.Context(), body.ParentRunID)
		if perr != nil {
			if errors.Is(perr, store.ErrNotFound) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("parent_run_id %s not found", body.ParentRunID))
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Errorf("get parent run: %w", perr))
			return
		}
		ancestors, err := tenant.GetRunAncestorPipelines(r.Context(), body.ParentRunID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("ancestor walk: %w", err))
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
				writeError(w, http.StatusUnprocessableEntity, errors.New("cloud RunAndAwait cannot inherit a working-tree source bundle; run the child locally or submit a new run with sparkwing pipeline trigger <pipeline> --profile <cloud-profile> --working-tree from the checkout"))
				return
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

	// safety: the run page and commit status read GITHUB_REPOSITORY while a
	// direct runner fetches git.repo_url, so a trigger that names two
	// repositories would show one and run the other.
	if _, err := sourceurl.TriggerRepository(body.Git.RepoURL, body.Trigger.Env["GITHUB_REPOSITORY"],
		body.Git.GithubOwner, body.Git.GithubRepo); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
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
		SourceKey:     sourceKey,
		SourceToken:   claimIdentity(r).TokenPrefix,
	}

	principal := s.floodKey(r, "pipeline:"+body.Pipeline)
	// safety: a redelivery is answered with the run it already started before
	// anything is charged, so repeating one never spends the submitter's cap.
	release, original, duplicate := s.claimSubmissionDigest(principal, intake)
	if duplicate {
		writeJSON(w, http.StatusConflict, triggerResp{RunID: original, Status: "duplicate"})
		return
	}
	if !s.admitTriggerSubmission(w, r, tenant, principal, body.Trigger.Source) {
		release()
		return
	}

	var hereToken *store.Token
	var rawHereToken string
	if here != nil {
		rawHereToken, hereToken, err = s.admitHereTrigger(triggerCtx, tenant, intake, *here)
	} else {
		err = s.admitTrigger(triggerCtx, tenant, intake)
	}
	if err != nil {
		release()
		if errors.Is(err, store.ErrSourceAlreadyBound) {
			writeError(w, http.StatusConflict, err)
			return
		}
		if s.writeComputeLimitRefusal(w, r, "", "", err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	resp := triggerResp{RunID: runID, Status: "dispatched"}
	if hereToken != nil {
		noteHereRunnerAudit(r.Context(), hereToken, here.Reason)
		resp.RunnerToken, resp.RunnerTokenExpiresAt = rawHereToken, hereToken.ExpiresAt.Unix()
	}
	writeJSON(w, http.StatusAccepted, resp)
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
	SourceKey      string
	SourceToken    string
}

// safety: every path that starts a run on this controller writes its three rows
// here -- the trigger, the pending run, the dispatch -- so an HTTP submission
// and a schedule the controller fired land identically.
func (s *Server) admitTrigger(ctx context.Context, t *store.Tenant, in triggerIntake) error {
	// safety: the trigger and the run it names are written together, so a guard
	// that refuses the run leaves no trigger behind for a worker to claim.
	trig, run := triggerRows(in)
	var err error
	if in.SourceKey != "" {
		err = t.CreateSourceTriggerWithRun(ctx, trig, run, in.SourceKey, in.User, in.SourceToken)
	} else {
		err = t.CreateTriggerWithRun(ctx, trig, run)
	}
	if err != nil {
		if errors.Is(err, store.ErrDuplicateIdempotencyKey) || errors.Is(err, store.ErrComputeLimit) {
			return err
		}
		if errors.Is(err, store.ErrSourceAlreadyBound) {
			return err
		}
		return fmt.Errorf("persist the trigger and its run: %w", err)
	}

	s.recordQueueActivity(in.At)
	return nil
}

func triggerRows(in triggerIntake) (store.Trigger, store.Run) {
	trig := store.Trigger{
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
	}
	run := store.Run{
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
		Invocation:    triggerRunInvocation(in.Env),
	}
	return trig, run
}

// safety: every host of a triggered run, the planning claim's included, reads the trigger's run flags and the
// default branch a GitHub App trigger recorded from the run row, so every intake that creates a run records them.
func triggerRunInvocation(env map[string]string) map[string]any {
	inv := runflags.FromTriggerEnv(env).Invocation()
	if branch := env[EnvDefaultBranch]; branch != "" {
		if inv == nil {
			inv = map[string]any{}
		}
		inv[runflags.DefaultBranchKey] = branch
	}
	return inv
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
		tenant, ok := s.requestTenant(w, r)
		if !ok {
			return
		}
		holder, err := tenant.TriggerClaimant(r.Context(), id)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	cancelled, err := tenant.HeartbeatTrigger(r.Context(), id, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if !errors.Is(err, store.ErrLockHeld) && !errors.Is(err, store.ErrInsufficientCredits) {
			s.logger.Error("heartbeat trigger failed", "trigger_id", id, "err", err)
		}
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return
	}
	writeJSON(w, http.StatusOK, heartbeatResp{CancelRequested: cancelled})
}

func (s *Server) handleFinishTrigger(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := tenant.FinishTrigger(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if trig, err := tenant.GetTrigger(r.Context(), id); err == nil && trig.Status == "failed" {
		if run, err := tenant.GetRun(r.Context(), id); err == nil {
			s.logger.Warn("trigger failed", "trigger_id", id, "pipeline", trig.Pipeline, "err", run.Error)
		}
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
	id, err := tenant.FindSpawnedChildTriggerID(r.Context(), parentRunID, parentNodeID, pipeline)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"run_id": id})
}

func (s *Server) handleListPendingTriggersForParent(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	parent := r.PathValue("id")
	if parent == "" {
		writeError(w, http.StatusBadRequest, errors.New("parent run id is required"))
		return
	}
	ids, err := tenant.ListPendingTriggersForParent(r.Context(), parent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"trigger_ids": ids})
}

func (s *Server) handleGetTrigger(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	tr, err := tenant.GetTrigger(r.Context(), id)
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

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	localDir := s.dashboard != nil && s.dashboard.Local && s.dashboard.Paths.Root != ""
	// safety: a laptop run's directory is named by its id, so an id that is not one path segment is refused
	// before anything is deleted rather than leaving its row gone and its directory behind.
	if err := storage.SafeSegment(id); localDir && err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// safety: the run's log records are the only index of its segments, so
	// the segments go first.
	if _, err := s.deleteRunSegments(r.Context(), s.logObjects(), tenant.Team(), id); err != nil {
		s.writeInternalError(w, r, "delete the run's log segments", err)
		return
	}
	if err := tenant.DeleteRun(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if localDir {
		if err := removeLocalRunDir(s.dashboard.Paths.RunsDir(), id); err != nil {
			s.writeInternalError(w, r, "remove the run's directory", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// safety: a laptop dashboard's node logs live in the run's directory on its own disk; os.Root keeps a planted
// symlink from turning the removal into one outside the runs tree.
func removeLocalRunDir(runsDir, runID string) error {
	root, err := os.OpenRoot(runsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(root.RemoveAll(runID), root.Close())
}

type claimPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type claimResources struct {
	Capacity     match.Resources `json:"capacity"`
	Availability match.Resources `json:"availability"`
}

// safety: an allow list that is present, even empty, binds; an absent one
// claims as it always has, and the runner refuses a disallowed run itself.
// The platform and resources steer scheduling only.
func claimProfileContext(ctx context.Context, allowRepos []string, platform *claimPlatform, resources *claimResources) (context.Context, match.Profile, error) {
	var profile match.Profile
	if allowRepos != nil {
		allow, err := sourceurl.ParseRepoAllowlist(allowRepos)
		if err != nil {
			return ctx, profile, fmt.Errorf("allow_repos: %w", err)
		}
		profile.Accept = allow
	}
	if platform != nil {
		profile.OS, profile.Arch = platform.OS, platform.Arch
	}
	if r := resources; r != nil {
		if r.Capacity.Cores < 0 || math.IsNaN(r.Capacity.Cores) || math.IsInf(r.Capacity.Cores, 0) || r.Capacity.MemoryBytes < 0 {
			return ctx, profile, errors.New("resources.capacity must be finite and non-negative")
		}
		profile.Capacity = r.Capacity
	}
	return store.WithClaimProfile(ctx, profile), profile, nil
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
	// safety: a plan snapshot is stored verbatim, so decoding does not bound its size.
	maxPlanSnapshotBody = store.MaxPlanBytes
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
	s.logger.Error(operation, append(requestLogAttrs(r), "err", err)...)
	writeError(w, http.StatusInternalServerError, err)
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	n, err := tenant.GetNode(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	public, err := s.publicNodesWithExecutionLocation(r.Context(), []*store.Node{n})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, public[0])
}

type claimNodeReq struct {
	HolderID   string          `json:"holder_id"`
	LeaseSecs  int             `json:"lease_secs,omitempty"`
	Labels     []string        `json:"labels,omitempty"`
	Headroom   *claimHeadroom  `json:"headroom,omitempty"`
	Capacity   *claimCapacity  `json:"capacity,omitempty"`
	AllowRepos []string        `json:"allow_repos,omitempty"`
	Platform   *claimPlatform  `json:"platform,omitempty"`
	Resources  *claimResources `json:"resources,omitempty"`
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

func (s *Server) recordAdvertisedHeadroom(r *http.Request, holderID string, h *claimHeadroom) {
	if h == nil {
		return
	}
	team := store.DefaultTeam
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil && store.NormalizeTeam(p.Team) != "" {
		team = store.NormalizeTeam(p.Team)
	}
	s.runnerHeadroom.record(presenceKey{tokenPrefix: claimIdentity(r).TokenPrefix, name: presenceName(holderID)}, runnerHeadroom{
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

type claimNamedNodeReq struct {
	HolderID  string `json:"holder_id"`
	LeaseSecs int    `json:"lease_secs"`
	// safety: without this the caller runs the node on an executor it already
	// has, so a metered claim is held to the class the warm pool serves.
	SizesToClass bool            `json:"sizes_to_class,omitempty"`
	Labels       []string        `json:"labels,omitempty"`
	AllowRepos   []string        `json:"allow_repos,omitempty"`
	Platform     *claimPlatform  `json:"platform,omitempty"`
	Resources    *claimResources `json:"resources,omitempty"`
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
	ctx, _, err := claimProfileContext(r.Context(), body.AllowRepos, body.Platform, body.Resources)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	n, err := s.store.ClaimNamedNode(ctx, claimIdentity(r), runID, nodeID,
		body.HolderID, time.Duration(body.LeaseSecs)*time.Second,
		store.NamedClaimOptions{SizesToClass: body.SizesToClass, Labels: body.Labels})
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
// game and anything else needs the run's dispatch claim. The store then checks
// the node's selector against the labels the claim sent.
func (s *Server) mayClaimNamedNode(w http.ResponseWriter, r *http.Request, runID, nodeID string) bool {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.HasScope(ScopeAdmin) {
		return true
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return false
	}
	n, err := tenant.GetNode(r.Context(), runID, nodeID)
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
	if tenant, err := s.tenantFor(r); err == nil {
		if run, err := tenant.GetRun(r.Context(), n.RunID); err == nil && run != nil {
			pipeline = run.Pipeline
		}
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
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
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	err := tenant.AcknowledgeNodeExecutionStart(r.Context(), runID, nodeID, claimIdentity(r), body)
	if errors.Is(err, store.ErrInvalidInput) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, store.ErrLockHeld) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		s.writeNodeStoreError(w, "acknowledge node execution start", runID, nodeID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFinishNodeExecutionAttempt(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
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
	err := tenant.FinishNodeExecutionAttempt(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), claimIdentity(r), body)
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

func (s *Server) handleMarkNodeReady(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	if err := tenant.MarkNodeReady(r.Context(), runID, nodeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	charge, metered, err := s.store.HeartbeatNodeClaimWithCredits(claimCtx, runID, nodeID, fence.Claimant, body.HolderID, lease, s.Metering())
	if err != nil {
		if !errors.Is(err, store.ErrLockHeld) {
			s.logger.Error("heartbeat node claim failed", "run_id", runID, "node_id", nodeID, "err", err)
		}
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return
	}
	// safety: the runner abandons a node whose claim the controller refuses,
	// which is how a cancellation for an empty balance reaches it.
	if charge.ForgivenSeconds > 0 {
		s.logger.Warn("charge cap engaged; the gap since the previous charge is not billed",
			"run_id", runID, "node_id", nodeID, "forgiven_s", charge.ForgivenSeconds)
	}
	if charge.Cancel {
		s.cancelForExhaustedCredits(r, runID, nodeID, fence.Claimant.TokenPrefix, charge)
	}
	if charge.Cancel || (metered && s.stopForWallClockLimit(r, runID, nodeID, fence.Claimant.TokenPrefix)) {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return
	}
	s.runnerHeartbeats.record(presenceKey{tokenPrefix: fence.Claimant.TokenPrefix, name: presenceName(body.HolderID)}, time.Now())
	// safety: a runner that predates the watched flag reads any answer but 204 as a refused renewal.
	if r.Header.Get(store.LogWatchHeader) == "1" {
		writeJSON(w, http.StatusOK, nodeHeartbeatResp{Watched: s.liveLogs.Watched(runID, nodeID)})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type nodeHeartbeatResp struct {
	Watched bool `json:"watched"`
}

func (s *Server) handleUpdateNodeActivity(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body struct {
		Detail string `json:"detail"`
	}
	_ = decodeJSON(r, &body)
	if err := tenant.UpdateNodeActivity(r.Context(), runID, nodeID, body.Detail); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListNodeSteps(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	steps, err := tenant.ListNodeSteps(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if steps == nil {
		steps = []*store.NodeStep{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
}

func (s *Server) writeNodeStoreError(w http.ResponseWriter, operation, runID, nodeID string, err error) {
	s.logger.Error(operation, "run_id", runID, "node_id", nodeID, "err", err)
	var sqlErr interface{ SQLState() string }
	if errors.As(err, &sqlErr) {
		switch sqlErr.SQLState() {
		case "40001", "40P01", "55P03":
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, err)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if err := tenant.CreateDebugPause(r.Context(), body); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
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
	events, err := tenant.ListEventsAfter(r.Context(), runID, afterSeq, limit)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	pauses, err := tenant.ListDebugPauses(r.Context(), runID)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	p, err := tenant.GetActiveDebugPause(r.Context(), runID, nodeID)
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
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if err := tenant.ReleaseDebugPause(r.Context(), runID, nodeID, releasedBy, body.ReleaseKind); err != nil {
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
