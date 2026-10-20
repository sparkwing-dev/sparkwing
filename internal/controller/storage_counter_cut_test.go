package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheCounterRoutesAreAbsent(t *testing.T) {
	server := New(nil, nil)
	handler := server.Handler()
	for _, path := range []string{"/internal/storage/reserve", "/internal/storage/commit", "/internal/storage/release", "/internal/downloads/charge", "/internal/egress/totals"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || server.RegisteredRoute(request) {
			t.Errorf("removed counter path %s = %d %s", path, response.Code, response.Body)
		}
	}
}
