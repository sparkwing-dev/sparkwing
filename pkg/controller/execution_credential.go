package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var executionNodePatterns = map[string]bool{
	"POST /api/v1/runs/{id}/nodes/{nodeID}/start":             true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/finish":            true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/deps":              true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/dispatch":          true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/logs":              true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/metrics":           true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/usage":             true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/execution-start":   true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/execution-finish":  true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/claim/validate":    true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat":         true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/activity":          true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/touch":             true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/annotations":       true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/summary":           true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest": true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start":       true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish":      true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip":        true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations": true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary":     true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/bounce/consume":    true,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/status":            true,
	"GET /api/v1/runs/{id}/nodes/{nodeID}/bounce":             true,
}

var executionConcurrencyPatterns = map[string]bool{
	"POST /api/v1/concurrency/{key}/acquire":   true,
	"POST /api/v1/concurrency/{key}/heartbeat": true,
	"POST /api/v1/concurrency/{key}/release":   true,
	"GET /api/v1/concurrency/{key}/holder":     true,
	"GET /api/v1/concurrency/{key}/state":      true,
	"GET /api/v1/concurrency/{key}/resolve":    true,
}

var executionProfilePatterns = map[string]bool{
	"GET /api/v1/pipelines/{name}/profile":               true,
	"POST /api/v1/pipelines/{name}/profile/observations": true,
	"POST /api/v1/pipelines/{name}/profile/contention":   true,
	"POST /api/v1/pipelines/{name}/profile/waits":        true,
}

func (s *Server) executionCredentialBoundary(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		binding, bound := executionBindingFromContext(r.Context())
		if !bound {
			next.ServeHTTP(w, r)
			return
		}
		_, pattern := mux.Handler(r)
		allowed, err := s.executionCredentialAllowsHTTP(r.Context(), r, pattern, *binding)
		if err != nil {
			if errors.Is(err, store.ErrLockHeld) {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !allowed {
			p, _ := PrincipalFromContext(r.Context())
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code: "credential_bound", Principal: p.label(),
				Message: "execution credential cannot access this controller resource",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) executionCredentialNodeRequest(
	w http.ResponseWriter,
	r *http.Request,
	binding store.ExecutionCredentialBinding,
	runID, nodeID string,
) (*http.Request, bool) {
	fence, err := nodeClaimFenceFromRequest(r)
	if err != nil || fence.HolderID != binding.HolderID || fence.ClaimGeneration != binding.ClaimGeneration {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return r, false
	}
	owned, err := s.store.ExecutionCredentialOwnsNode(r.Context(), binding, runID, nodeID, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return r, false
	}
	if !owned {
		p, _ := PrincipalFromContext(r.Context())
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "credential_bound", Principal: p.label(),
			Message: "execution credential does not own node " + runID + "/" + nodeID,
		})
		return r, false
	}
	execution := store.ExecutionCredentialFence{Binding: binding, Fence: fence}
	ctx := store.WithExecutionCredentialFence(store.WithNodeClaimFence(r.Context(), fence), execution)
	return r.WithContext(ctx), true
}

func (s *Server) executionCredentialAllowsHTTP(
	ctx context.Context,
	r *http.Request,
	pattern string,
	binding store.ExecutionCredentialBinding,
) (bool, error) {
	switch pattern {
	case "GET /api/v1/auth/whoami", "GET /api/v1/services":
		return true, nil
	}
	live, err := s.store.ExecutionCredentialBindingIsLive(ctx, binding, time.Now())
	if err != nil {
		return false, err
	}
	if !live {
		return false, store.ErrLockHeld
	}
	runID := matchedRouteValue(pattern, r.URL.EscapedPath(), "id")
	nodeID := matchedRouteValue(pattern, r.URL.EscapedPath(), "nodeID")
	switch pattern {
	case "GET /api/v1/runs/{id}":
		return s.executionCredentialAllowsRun(ctx, binding, runID)
	case "GET /api/v1/triggers/{id}":
		return runID == binding.RunID, nil
	case "GET /api/v1/runs/{id}/nodes/{nodeID}":
		return s.store.ExecutionCredentialOwnsNode(ctx, binding, runID, nodeID, time.Now())
	case "GET /api/v1/runs/{id}/nodes/{nodeID}/output":
		return s.executionCredentialAllowsOutput(ctx, binding, runID, nodeID)
	case "POST /api/v1/runs/{id}/nodes", "POST /api/v1/runs/{id}/events",
		"POST /api/v1/runs/{id}/heartbeat":
		return runID == binding.RunID, nil
	case "POST /api/v1/triggers", "GET /api/v1/triggers/spawned-child":
		return true, nil
	case "GET /api/v1/secrets/{name}":
		return r.URL.Query().Get("run") == binding.RunID, nil
	case "POST /api/v1/runs/{id}/gitcache/git/register",
		"GET /api/v1/runs/{id}/gitcache/git/{path...}",
		"POST /api/v1/runs/{id}/gitcache/git/{path...}":
		return runID == binding.RunID, nil
	}
	if executionNodePatterns[pattern] {
		return s.store.ExecutionCredentialOwnsNode(ctx, binding, runID, nodeID, time.Now())
	}
	if executionConcurrencyPatterns[pattern] {
		return true, nil
	}
	if executionProfilePatterns[pattern] {
		pipeline := matchedRouteValue(pattern, r.URL.EscapedPath(), "name")
		run, err := s.store.GetRun(ctx, binding.RunID)
		return err == nil && pipeline == run.Pipeline, err
	}
	return false, nil
}

func (s *Server) executionCredentialAllowsOutput(
	ctx context.Context,
	binding store.ExecutionCredentialBinding,
	runID, nodeID string,
) (bool, error) {
	if runID == binding.RunID {
		if owned, err := s.store.ExecutionCredentialOwnsNode(ctx, binding, runID, nodeID, time.Now()); err != nil || owned {
			return owned, err
		}
		root, err := s.store.GetNode(ctx, binding.RunID, binding.RootNodeID)
		if err != nil {
			return false, err
		}
		for _, dependency := range root.Deps {
			if dependency == nodeID {
				return true, nil
			}
		}
		return false, nil
	}
	trigger, err := s.store.GetTrigger(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if trigger.ParentRunID != binding.RunID || trigger.RequestedOutputNodeID == "" ||
		trigger.RequestedOutputNodeID != nodeID {
		return false, nil
	}
	return s.store.ExecutionCredentialOwnsNode(ctx, binding, binding.RunID, trigger.ParentNodeID, time.Now())
}

func matchedRouteValue(pattern, escapedPath, name string) string {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = pattern[i+1:]
	}
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(escapedPath, "/"), "/")
	for i, part := range patternParts {
		if part != "{"+name+"}" || i >= len(pathParts) {
			continue
		}
		value, err := url.PathUnescape(pathParts[i])
		if err == nil {
			return value
		}
	}
	return ""
}
