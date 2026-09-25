package controller

import (
	"encoding/json"
	"net/http"
)

// ServicesResponse is the wire shape of GET /api/v1/services. Names
// describe what each URL serves; absent fields signal "not configured"
// (clients fall back to whatever explicit config they have).
type ServicesResponse struct {
	// DataDownloadURL is the controller route that signs one team object.
	// Empty means clients keep reading from the cache service.
	DataDownloadURL string `json:"data_download_url,omitempty"`
	// CachePod is the externally-reachable URL of the sparkwing-cache
	// pod (gitcache + artifact store + registry proxy + upload sync).
	// An off-cluster runner holding a run's cache grant reads source, the
	// binary cache and artifacts there directly, and the operator CLI
	// refreshes and seeds mirrors there before a dispatch. Empty when the
	// controller wasn't started with --cache-pod-url.
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

	// Dashboard is the externally-reachable URL a human opens to watch
	// this controller's runs. `sparkwing cloud connect` prints it, so an
	// operator who has just connected reaches the web view without being
	// told the address out of band. Empty when the controller wasn't
	// started with --dashboard-url.
	Dashboard string `json:"dashboard,omitempty"`

	// MultiTeam reports a controller that may hold more than one team. The
	// cache's refresh and seed routes take the cache's operator token, which
	// no team member holds there, so a CLI skips them.
	MultiTeam bool `json:"multi_team,omitempty"`
	// DirectData advertises S3-backed signed data routes for claimed runs.
	DirectData bool `json:"direct_data,omitempty"`
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	multiTeam := s.MultiTeam()
	downloadURL := s.dataDownloadURL()
	if downloadViaIngress(r) && s.downloadCDN == nil {
		downloadURL = ""
	}
	if s.cachePodURL == "" && s.logsURL == "" && s.dashboardURL == "" && !multiTeam && downloadURL == "" && s.directUploads == nil {
		http.Error(w, "no services announced", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ServicesResponse{
		DataDownloadURL: downloadURL,
		DirectData:      s.directUploads != nil,
		CachePod:        s.cachePodURL,
		Logs:            s.logsURL,
		Dashboard:       s.dashboardURL,
		MultiTeam:       multiTeam,
	})
}
