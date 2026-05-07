package local

import (
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
)

// SetArtifactStore enables the /api/v1/artifacts/{key} read route.
// Nil (the default) makes the handler return 404.
func (s *Server) SetArtifactStore(a storage.ArtifactStore) {
	s.artifactStore = a
}

// handleArtifactGet streams the artifact at {key} to the response.
// Returns 404 when no ArtifactStore is configured or the key is
// missing.
func (s *Server) handleArtifactGet(w http.ResponseWriter, r *http.Request) {
	if s.artifactStore == nil {
		http.NotFound(w, r)
		return
	}
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
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
