package controller

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var claimWorkKinds = []store.ClaimTokenKind{store.ClaimTokenWork}

// safety: a runner's node claim ends when its node reports, before the runner seals the node's log, so the log
// routes take it as a claim, whose tail ClaimLogTail allows, rather than as the runner it acts for.
var claimLogKinds = []store.ClaimTokenKind{store.ClaimTokenWork, store.ClaimTokenNode}

// safety: every claim that runs a node's code reads that node's secrets
// through its own claim, under the plan's declaration and with an event.

type claimHeartbeatReq struct {
	LeaseSecs int `json:"lease_secs"`
}

type claimBeatResp struct {
	Cancel  bool `json:"cancel"`
	Watched bool `json:"watched"`
}

// safety: a refused beat is 409 and renews nothing, which is how a claim that
// was lost, superseded or can no longer be paid for reaches the pod.
func (s *Server) handleClaimHeartbeat(w http.ResponseWriter, r *http.Request) {
	tok, _ := claimTokenFromContext(r.Context())
	var req claimHeartbeatReq
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	beat, err := s.store.HeartbeatClaim(r.Context(), tok, time.Duration(req.LeaseSecs)*time.Second, time.Now())
	if beat.Charge.Cancel {
		if tenant, terr := s.tenantForTeam(r.Context(), tok.Team); terr != nil {
			s.logger.Error("cancelling a run for exhausted credits failed", "run_id", tok.RunID, "err", terr)
		} else {
			if _, aerr := tenant.AppendEventOnce(r.Context(), tok.RunID, tok.NodeID, store.EventKindCreditsExhausted, nil); aerr != nil {
				s.logger.Warn("recording an exhausted-credit cancellation failed", "run_id", tok.RunID, "err", aerr)
			}
			if cerr := tenant.RequestCancel(r.Context(), tok.RunID); cerr != nil {
				s.logger.Error("cancelling a run for exhausted credits failed", "run_id", tok.RunID, "err", cerr)
			}
		}
	}
	switch {
	case errors.Is(err, store.ErrLockHeld):
		writeError(w, http.StatusConflict, store.ErrLockHeld)
	case err != nil:
		s.writeInternalError(w, r, "claim heartbeat", err)
	default:
		writeJSON(w, http.StatusOK, claimBeatResp{Cancel: beat.Cancel, Watched: s.liveLogs.Watched(tok.RunID, tok.NodeID)})
	}
}

type claimExecutionResp struct {
	SpecHash string `json:"spec_hash"`
}

func (s *Server) handleClaimExecutionStart(w http.ResponseWriter, r *http.Request) {
	tok, _ := claimTokenFromContext(r.Context())
	hash, err := s.store.StartClaimExecution(r.Context(), tok, time.Now())
	if err != nil {
		writeClaimRefusal(w, r, s, err)
		return
	}
	writeJSON(w, http.StatusOK, claimExecutionResp{SpecHash: hash})
}

type childRunReq struct {
	Ordinal  int64             `json:"ordinal"`
	Pipeline string            `json:"pipeline"`
	Args     map[string]string `json:"args,omitempty"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
}

// safety: the child builds the parent's own commit, read from the parent's
// trigger, so a node cannot point a child at a repository its run was not
// admitted for.
func (s *Server) handleEnqueueChildRun(w http.ResponseWriter, r *http.Request) {
	tok, _ := claimTokenFromContext(r.Context())
	var req childRunReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var parent *store.Trigger
	tenant, err := s.tenantForTeam(r.Context(), tok.Team)
	if err == nil {
		parent, err = tenant.GetTrigger(r.Context(), tok.RunID)
	}
	if err != nil || store.NormalizeTeam(parent.Team) != tok.Team {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return
	}
	if (req.Repo != "" && req.Repo != parent.Repo) || (req.Branch != "" && req.Branch != parent.GitBranch) {
		writeError(w, http.StatusUnprocessableEntity,
			errors.New("a controller-dispatched node starts children of its own repository and commit only"))
		return
	}
	ancestors, err := tenant.GetRunAncestorPipelines(r.Context(), tok.RunID)
	if err != nil {
		s.writeInternalError(w, r, "ancestor walk", err)
		return
	}
	if slices.Contains(append(ancestors, parent.Pipeline), req.Pipeline) {
		writeError(w, http.StatusConflict, fmt.Errorf("cycle: %s would re-enter itself", req.Pipeline))
		return
	}
	now := time.Now()
	childID, err := s.store.EnqueueChildRun(r.Context(), tok, req.Ordinal, store.Trigger{
		ID: newRunID(), Pipeline: req.Pipeline, Args: req.Args, TriggerSource: "await-pipeline",
		TriggerEnv: parent.TriggerEnv, GitBranch: parent.GitBranch, GitSHA: parent.GitSHA,
		Repo: parent.Repo, RepoURL: parent.RepoURL, GithubOwner: parent.GithubOwner, GithubRepo: parent.GithubRepo,
		RepoInherited: true, CreatedAt: now,
	}, now)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, triggerResp{RunID: childID, Status: "dispatched"})
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, store.ErrClaimResultConflict):
		writeError(w, http.StatusConflict, err)
	case s.writeComputeLimitRefusal(w, r, "", "", err):
	default:
		writeClaimRefusal(w, r, s, err)
	}
}

// safety: a claim reads only the children its own node started, and only
// their redacted form and finished outputs.
func (s *Server) claimChild(w http.ResponseWriter, r *http.Request) (string, bool) {
	tok, _ := claimTokenFromContext(r.Context())
	childID := r.PathValue("childID")
	ok, err := s.store.IsChildRunOf(r.Context(), tok.Team, tok.RunID, tok.NodeID, childID)
	if err != nil {
		s.writeInternalError(w, r, "child run", err)
		return "", false
	}
	if !ok {
		writeError(w, http.StatusNotFound, runNotFound(childID))
	}
	return childID, ok
}

func (s *Server) handleGetChildRun(w http.ResponseWriter, r *http.Request) {
	childID, ok := s.claimChild(w, r)
	if !ok {
		return
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	run, err := tenant.GetRun(r.Context(), childID)
	if err != nil {
		writeError(w, http.StatusNotFound, runNotFound(childID))
		return
	}
	writeJSON(w, http.StatusOK, store.RedactedRun(run))
}

func (s *Server) handleGetChildNodeOutput(w http.ResponseWriter, r *http.Request) {
	childID, ok := s.claimChild(w, r)
	if !ok {
		return
	}
	r.SetPathValue("id", childID)
	s.handleGetNodeOutput(w, r)
}

// safety: a work claim reads another run's output only through a reference
// its plan declares, resolved here, so it never names the run it reads; a
// refused reference is recorded with the claim that asked.
func (s *Server) handleClaimInput(w http.ResponseWriter, r *http.Request) {
	tok, _ := claimTokenFromContext(r.Context())
	var req store.ClaimInputRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	in, err := s.store.ResolveClaimInput(r.Context(), tok, req, time.Now())
	switch {
	case err == nil:
		grant, ok := s.outputGrant(w, r, tok.Team, in.RunID, in.NodeID)
		if !ok {
			return
		}
		in.Output = grant
		writeJSON(w, http.StatusOK, in)
	case errors.Is(err, store.ErrInputUndeclared):
		s.logger.WarnContext(r.Context(), "audit", append(requestLogAttrs(r), "event", "input_undeclared",
			"principal_kind", "claim", "principal_id", tok.Prefix, "team", string(tok.Team),
			"run_id", tok.RunID, "node_id", tok.NodeID, "input_kind", string(req.Kind))...)
		writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "input_undeclared", Message: err.Error()})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	default:
		s.writeInternalError(w, r, "claim input", err)
	}
}

// safety: binds a route whose run is named in its body or by a holder, so the
// handler behind it must check that run against the claim's own.
func ownClaimBinding(r *http.Request) (string, string) {
	tok, _ := claimTokenFromContext(r.Context())
	return tok.RunID, tok.NodeID
}

var errClaimOIDCUnavailable = errors.New(
	"OIDC tokens are not yet issued to controller-dispatched nodes; they arrive with Sparkwing Cloud OIDC")

func handleClaimOIDCToken(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusUnprocessableEntity, errClaimOIDCUnavailable)
}
