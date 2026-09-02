package controller

import (
	"errors"
	"io"
	"net/http"

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
