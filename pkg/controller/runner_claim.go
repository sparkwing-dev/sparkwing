package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a node claim token acts as its runner confined to the claim: its own
// run, writes to its own node, the claim's fence and no trigger generation on
// every request, and it stops with the claim or the runner's credential. The
// runner's bearer therefore never leaves the runner.
func (s *Server) serveRunnerClaim(w http.ResponseWriter, r *http.Request, rc store.RunnerClaim, next http.Handler) {
	grant, reaches := "", runnerClaimReaches(r, rc.Token)
	if !reaches {
		var err error
		if grant, reaches, err = s.runnerClaimCrossRun(r, rc.Token); err != nil {
			s.writeInternalError(w, r, "node claim token: resolve a cross-run route", err)
			return
		}
	}
	if !reaches {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_mismatch",
			Message: "a node claim token reaches only its own run, writes only to its own node, and beyond them " +
				"reads its team's pipeline references and starts and awaits its own node's child runs",
		})
		return
	}
	p := s.runnerClaimPrincipal(rc, time.Now())
	if p == nil {
		writeAuthError(w, http.StatusUnauthorized, authErrorBody{
			Code:    "unauthenticated",
			Message: "the credential that made this claim no longer authenticates",
		})
		return
	}
	if grant != "" {
		p.Scopes = append(p.Scopes, grant)
	}
	bindRunnerClaimFence(r, rc.Fence)
	observeRequestPrincipal(p.Kind)
	ctx := context.WithValue(contextWithPrincipal(r.Context(), p), runnerClaimCtxKey{}, rc)
	stampPrincipal(ctx, p)
	noteAuditTarget(ctx, "claim_token", rc.Token.Prefix)
	next.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) runnerClaimPrincipal(rc store.RunnerClaim, now time.Time) *Principal {
	tok, err := s.store.LookupTokenByPrefix(rc.Claimant.TokenPrefix)
	if err != nil || tok == nil || tok.Team != rc.Token.Team || tok.Principal != rc.Claimant.Principal || !tok.IsValid(now) {
		return nil
	}
	return &Principal{
		Name:        tok.Principal,
		Kind:        store.ActingKind(tok.Kind, tok.Principal),
		Scopes:      runnerClaimScopes(tok.Scopes),
		TokenPrefix: tok.Prefix,
		Authed:      now,
		Team:        tok.Team,
		Expires:     credentialEnd(tok.ExpiresAt, tok.RevokedAt),
	}
}

type runnerClaimCtxKey struct{}

func runnerClaimFromContext(ctx context.Context) (store.RunnerClaim, bool) {
	rc, ok := ctx.Value(runnerClaimCtxKey{}).(store.RunnerClaim)
	return rc, ok
}

// safety: admin is dropped, so the claim's fence binds even when an operator
// token made the claim; what is left is what a node does under its claim.
var runnerClaimScopeSet = []string{ScopeNodesClaim, ScopeRunsState}

func runnerClaimScopes(held []string) []string {
	admin := false
	for _, s := range held {
		admin = admin || s == ScopeAdmin
	}
	var out []string
	for _, want := range runnerClaimScopeSet {
		for _, s := range held {
			if s == want || admin {
				out = append(out, want)
				break
			}
		}
	}
	return out
}

func bindRunnerClaimFence(r *http.Request, fence store.NodeClaimFence) {
	r.Header.Del(store.TriggerGenerationHeader)
	r.Header.Set(store.ClaimHolderHeader, fence.HolderID)
	r.Header.Set(store.ClaimMembershipHeader, fence.MembershipID)
	r.Header.Set(store.ClaimReservationHeader, fence.ReservationID)
	r.Header.Set(store.ClaimGenerationHeader, strconv.FormatInt(fence.ClaimGeneration, 10))
}

// safety: these are the run-level writes a node makes; every other write
// under the run is a dispatcher's, which a node claim never acts as. The
// workload token route checks the claim again before it mints.
var runnerClaimRunWrites = map[string]bool{
	"/events": true, "/heartbeat": true, "/cache-grant": true, "/oidc-token": true,
}

func runnerClaimReaches(r *http.Request, tok store.ClaimToken) bool {
	path := r.URL.EscapedPath()
	run := url.PathEscape(tok.RunID)
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case read && (path == "/api/v1/triggers/"+run || path == "/api/v1/services"):
		return true
	case strings.HasPrefix(path, "/api/v1/concurrency/"):
		return runnerClaimSlot(r, tok)
	}
	rest, ok := strings.CutPrefix(path, "/api/v1/runs/"+run)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return false
	}
	if read {
		return true
	}
	if node, ok := strings.CutPrefix(rest, "/nodes/"); ok {
		segment, _, _ := strings.Cut(node, "/")
		return segment == url.PathEscape(tok.NodeID)
	}
	return runnerClaimRunWrites[rest]
}

// safety: D-SEC-08. A node's Ref.Get and RunAndAwait reach these routes and
// no others beyond its run, each with the one scope the route asks, in the
// claim's team, which the handlers take from the principal: a pipeline's latest
// run, a run's node output, and its own node's child runs and their lookup.
func (s *Server) runnerClaimCrossRun(r *http.Request, tok store.ClaimToken) (string, bool, error) {
	path := r.URL.EscapedPath()
	if r.Method == http.MethodPost {
		var body struct {
			ParentRunID  string `json:"parent_run_id"`
			ParentNodeID string `json:"parent_node_id"`
			RetryOf      string `json:"retry_of"`
		}
		if path != "/api/v1/triggers" || !peekClaimBody(r, &body) ||
			body.ParentRunID != tok.RunID || body.ParentNodeID != tok.NodeID {
			return "", false, nil
		}
		if body.RetryOf == "" {
			return ScopeRunsWrite, true, nil
		}
		ok, err := s.runnerClaimChild(r.Context(), tok, body.RetryOf)
		return ScopeRunsWrite, ok, err
	}
	if r.Method != http.MethodGet {
		return "", false, nil
	}
	if path == "/api/v1/triggers/spawned-child" {
		q := r.URL.Query()
		if q.Get("parent_node_id") != tok.NodeID {
			return "", false, nil
		}
		ok, err := s.runnerClaimParent(r.Context(), tok, q.Get("parent_run_id"))
		return ScopeTriggersRead, ok, err
	}
	if rest, ok := strings.CutPrefix(path, "/api/v1/pipelines/"); ok {
		name, latest := strings.CutSuffix(rest, "/latest")
		return ScopeRunsRead, latest && name != "" && !strings.Contains(name, "/"), nil
	}
	rest, ok := strings.CutPrefix(path, "/api/v1/runs/")
	parts := strings.Split(rest, "/")
	switch {
	case !ok || parts[0] == "":
		return "", false, nil
	case len(parts) == 4 && parts[1] == "nodes" && parts[2] != "" && parts[3] == "output":
		return ScopeRunsRead, true, nil
	case len(parts) != 1 || r.URL.RawQuery != "":
		return "", false, nil
	}
	child, err := false, error(nil)
	if run, bad := url.PathUnescape(parts[0]); bad == nil {
		child, err = s.runnerClaimChild(r.Context(), tok, run)
	}
	return ScopeRunsRead, child, err
}

func (s *Server) runnerClaimChild(ctx context.Context, tok store.ClaimToken, run string) (bool, error) {
	tn, err := s.store.ForTeam(ctx, tok.Team)
	if err != nil {
		return false, err
	}
	child, err := tn.GetTrigger(ctx, run)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil || child.ParentNodeID != tok.NodeID {
		return false, err
	}
	return s.runnerClaimParent(ctx, tok, child.ParentRunID)
}

// safety: a retried node reaches the children its source attempt's node
// started as well as its own, so a parent is the claim's run or the run that
// run retries.
func (s *Server) runnerClaimParent(ctx context.Context, tok store.ClaimToken, run string) (bool, error) {
	if run == "" || run == tok.RunID {
		return run != "", nil
	}
	tn, err := s.store.ForTeam(ctx, tok.Team)
	if err != nil {
		return false, err
	}
	own, err := tn.GetRun(ctx, tok.RunID)
	if err != nil {
		return false, err
	}
	return own.RetryOf != "" && run == own.RetryOf, nil
}

func peekClaimBody(r *http.Request, into any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return err == nil && len(raw) <= 1<<20 && json.Unmarshal(raw, into) == nil
}

// safety: a slot names its run and holder in the query or the body, and a
// holder id is run/node, so the claim reaches only its own node's slots.
func runnerClaimSlot(r *http.Request, tok store.ClaimToken) bool {
	holder := tok.RunID + "/" + tok.NodeID
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	q := r.URL.Query()
	switch action {
	case "holder":
		return r.Method == http.MethodGet && q.Get("holder_id") == holder
	case "resolve":
		return r.Method == http.MethodGet && q.Get("run_id") == tok.RunID && q.Get("node_id") == tok.NodeID
	}
	var slot struct {
		HolderID          string `json:"holder_id"`
		InheritedHolderID string `json:"inherited_holder_id"`
		RunID             string `json:"run_id"`
		NodeID            string `json:"node_id"`
	}
	if r.Method != http.MethodPost || !peekClaimBody(r, &slot) {
		return false
	}
	switch action {
	case "cancel-waiter":
		return slot.RunID == tok.RunID && slot.NodeID == tok.NodeID
	case "acquire":
		return slot.HolderID == holder && slot.RunID == tok.RunID && slot.NodeID == tok.NodeID && slot.InheritedHolderID == ""
	case "heartbeat", "release":
		return slot.HolderID == holder
	}
	return false
}

// safety: a launcher's token answers only a route written for its kind; a
// route that names node tokens serves them as claims rather than as runners.
func (s *Server) serveClaimBearer(w http.ResponseWriter, r *http.Request, raw string, route *claimTokenRoute,
	next, byToken http.Handler,
) {
	if route == nil || !route.accepts(store.ClaimTokenNode) {
		rc, err := s.store.AuthorizeRunnerClaim(r.Context(), raw, time.Now())
		switch {
		case err == nil:
			s.serveRunnerClaim(w, r, rc, next)
			return
		case route == nil && errors.Is(err, store.ErrClaimNotRunner):
			byToken.ServeHTTP(w, r)
			return
		case route == nil || !(errors.Is(err, store.ErrClaimNotRunner) || errors.Is(err, store.ErrClaimTokenInvalid)):
			writeClaimRefusal(w, r, s, err)
			return
		}
	}
	s.serveClaim(w, r, raw, route, next)
}
