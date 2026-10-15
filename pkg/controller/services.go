package controller

import (
	"encoding/json"
	"net/http"
)

// ServicesResponse is the wire shape of GET /api/v1/services. Names
// describe what each URL serves; absent fields signal "not configured"
// (clients fall back to whatever explicit config they have).
type ServicesResponse struct {
	// Dashboard is the externally-reachable URL a human opens to watch
	// this controller's runs. `sparkwing cloud connect` prints it, so an
	// operator who has just connected reaches the web view without being
	// told the address out of band. Empty when the controller wasn't
	// started with --dashboard-url.
	Dashboard string `json:"dashboard,omitempty"`

	// MultiTeam reports a controller that may hold more than one team.
	MultiTeam bool `json:"multi_team,omitempty"`
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	multiTeam := s.MultiTeam()
	if s.dashboardURL == "" && !multiTeam {
		http.Error(w, "no services announced", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ServicesResponse{
		Dashboard: s.dashboardURL,
		MultiTeam: multiTeam,
	})
}
