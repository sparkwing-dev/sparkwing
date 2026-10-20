package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyManifestEndpointIsGone(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run/nodes/job/artifact-manifest", strings.NewReader(`{"manifest_digest":"digest"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(nil, nil).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed manifest endpoint: %d %s", response.Code, response.Body.String())
	}
}
