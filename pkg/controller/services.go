package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ServicesResponse is the wire shape of GET /api/v1/services. Names
// describe what each URL serves; absent fields signal "not configured"
// (clients fall back to whatever explicit config they have).
type ServicesResponse struct {
	// DataDownloadURL names the authenticated data download endpoint.
	DataDownloadURL string `json:"data_download_url,omitempty"`
	// Dashboard is the controller's externally reachable web URL.
	Dashboard string `json:"dashboard,omitempty"`

	// MultiTeam reports a controller that may hold more than one team.
	MultiTeam bool `json:"multi_team,omitempty"`
	// DirectData advertises capability-authorized upload and commit routes.
	DirectData bool `json:"direct_data,omitempty"`
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	multiTeam := s.MultiTeam()
	downloadURL := s.dataDownloadURL()
	if s.dashboardURL == "" && !multiTeam && downloadURL == "" && !s.dataUploadsAvailable() {
		return &apierr.Error{Code: apierr.NotFound, Message: "no services announced"}
	}
	writeJSON(w, http.StatusOK, ServicesResponse{
		DataDownloadURL: downloadURL,
		DirectData:      s.dataUploadsAvailable(),
		Dashboard:       s.dashboardURL,
		MultiTeam:       multiTeam,
	})
	return nil
}

func (s *Server) handleDirectCapabilities(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if !s.dataUploadsAvailable() {
		return &apierr.Error{Code: apierr.NotFound, Message: "data uploads are unavailable"}
	}
	download := s.dataDownloadURL() != ""
	writeJSON(w, http.StatusOK, map[string]bool{"upload": true, "source": download, "download": download})
	return nil
}

func (s *Server) dataUploadsAvailable() bool {
	d := s.dataController
	return d != nil && d.ResolveAuthority != nil && d.Access != nil && d.Objects != nil && (d.Store != nil || d.ResolveTenant != nil)
}

func (s *Server) dataDownloadURL() string {
	if s.dataController == nil || s.dataController.ResolveAuthority == nil || s.dataController.ReadBlob == nil ||
		s.dataController.Access == nil || s.dataController.Objects == nil || s.dataController.ChargeDownload == nil {
		return ""
	}
	return "/api/v1/data/download"
}
