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

	// Logs is the externally-reachable URL of the sparkwing-logs
	// service. Runners post node log lines there.
	//
	// It is announced because a controller and a logs service are two
	// binaries on two ports and only the second routes /api/v1/logs, so
	// a runner that assumed the controller's own URL posted every line
	// into a 404. Empty when the controller wasn't started with
	// --logs-url; a co-located deployment (the laptop dashboard mounts
	// both on one mux) needs no announcement, because there the
	// controller URL is already the right answer.
	Logs string `json:"logs,omitempty"`
}

func (s *Server) handleServices(w http.ResponseWriter, _ *http.Request) {
	if s.cachePodURL == "" && s.logsURL == "" {
		http.Error(w, "no services announced", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ServicesResponse{
		CachePod: s.cachePodURL,
		Logs:     s.logsURL,
	})
}
