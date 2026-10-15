package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCacheGrantRouteIsAbsent(t *testing.T) {
	server := &Server{}
	authed, public := server.routers(nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run/cache-grant", nil)
	for _, mux := range []*http.ServeMux{authed, public} {
		if _, pattern := mux.Handler(request); pattern != "" {
			t.Errorf("removed cache grant route resolves to %s", pattern)
		}
	}
}

func TestServicesDoNotAnnounceRemovedDataRoutes(t *testing.T) {
	server := &Server{dashboardURL: "https://controller.invalid", directUploads: &directUploadS3{}}
	response := httptest.NewRecorder()
	server.handleServices(response, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "data_download_url") || strings.Contains(response.Body.String(), "direct_data") {
		t.Fatalf("services = %d: %s", response.Code, response.Body)
	}
}
