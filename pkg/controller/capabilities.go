package controller

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func (s *Server) handleArtifactGet(w http.ResponseWriter, r *http.Request) {
	if s.artifactStore == nil {
		// safety: handler registered only via route gate; direct calls mirror gated behavior
		http.NotFound(w, r)
		return
	}
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	if !safeArtifactKey(key) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}
	if !s.artifactKeyReadable(w, r, key) {
		return
	}
	rc, err := s.artifactStore.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, rc)
}

// safety: ServeMux unescapes %2f inside {key}, so a traversal or
// double-encoded key is rejected here before any backend joins it to a
// path or object key.
func safeArtifactKey(key string) bool {
	return storage.SafeArtifactKey(key) == nil
}

// artifactKeyReadable holds the shared artifact store to the team boundary. A
// key under runs/<id>/ belongs to that run and is read only by its team,
// answering 404 like any other team's run. Every other key is content
// addressed and names no run, so nothing proves which team it belongs to;
// only the operator reads those here, and a node stages its own artifacts
// through its claim rather than this route.
func (s *Server) artifactKeyReadable(w http.ResponseWriter, r *http.Request, key string) bool {
	if runID, ok := strings.CutPrefix(key, "runs/"); ok {
		runID, _, _ = strings.Cut(runID, "/")
		t, ok := s.requestTenant(w, r)
		if !ok {
			return false
		}
		owned, err := t.OwnsRun(r.Context(), runID)
		if err != nil {
			s.writeInternalError(w, r, "artifact run team", err)
			return false
		}
		if !owned {
			http.NotFound(w, r)
			return false
		}
		return true
	}
	if p, ok := PrincipalFromContext(r.Context()); ok && !p.HasScope(ScopeAdmin) {
		http.NotFound(w, r)
		return false
	}
	return true
}
