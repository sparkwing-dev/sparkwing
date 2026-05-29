package controller

import (
	"encoding/json"
	"net/http"
)

// ServicesResponse is the wire shape of GET /api/v1/services. Names
// describe what each URL serves; absent fields signal "not configured"
// (clients fall back to whatever explicit config they have).
type ServicesResponse struct {
	// CachePod is the externally-reachable URL of the sparkwing-cache
	// pod (gitcache + artifact store + registry proxy + upload sync).
	// Operator CLI uses this for `sparkwing push` and the eager-refresh
	// on dispatch. Empty when the controller wasn't started with
	// --cache-pod-url.
	CachePod string `json:"cache_pod,omitempty"`
}

// handleServices answers GET /api/v1/services with the controller's
// announced auxiliary-service URLs. No auth required: the cache pod's
// URL is something the operator needs to reach directly anyway, and
// publishing it doesn't leak any secrets the operator wouldn't already
// have via their profile bundle.
//
// Returns 404 when the controller has no services configured -- the
// client treats this as "discovery unavailable" and may fall back to
// explicit profile config (none, in v0.6+).
func (s *Server) handleServices(w http.ResponseWriter, _ *http.Request) {
	if s.cachePodURL == "" {
		http.Error(w, "no services announced", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ServicesResponse{
		CachePod: s.cachePodURL,
	})
}
