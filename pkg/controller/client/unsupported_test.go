package client_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestUnregisteredRouteAnswersCanonicalNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	controller.New(nil, nil).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type = %q, want application/json", got)
	}
	var body apierr.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error == nil || body.Error.Code != apierr.NotFound {
		t.Fatalf("error = %+v, want not_found", body.Error)
	}
}

func TestRegisteredPathKeepsItsMethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	controller.New(nil, nil).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/runs", nil))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for a path registered under other methods", resp.StatusCode)
	}
	if resp.Header.Get("Allow") == "" {
		t.Error("405 answer carries no Allow header")
	}
}
