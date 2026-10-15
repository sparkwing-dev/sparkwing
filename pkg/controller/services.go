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
	// Empty means signed data downloads are unavailable.
	DataDownloadURL string `json:"data_download_url,omitempty"`
	// Dashboard is the externally-reachable URL a human opens to watch
	// this controller's runs. `sparkwing cloud connect` prints it, so an
	// operator who has just connected reaches the web view without being
	// told the address out of band. Empty when the controller wasn't
	// started with --dashboard-url.
	Dashboard string `json:"dashboard,omitempty"`

	// MultiTeam reports a controller that may hold more than one team.
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
	if s.dashboardURL == "" && !multiTeam && downloadURL == "" && s.directUploads == nil {
		http.Error(w, "no services announced", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ServicesResponse{
		DataDownloadURL: downloadURL,
		DirectData:      s.directUploads != nil,
		Dashboard:       s.dashboardURL,
		MultiTeam:       multiTeam,
	})
}
