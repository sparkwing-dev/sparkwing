package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheCounterRoutesAreAbsent(t *testing.T) {
	server := &Server{}
	authed, public := server.routers()
	for _, path := range []string{"/internal/storage/reserve", "/internal/storage/commit", "/internal/storage/release", "/internal/downloads/charge", "/internal/egress/totals"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		for _, mux := range []*http.ServeMux{authed, public} {
			if _, pattern := mux.Handler(request); pattern != "" {
				t.Errorf("removed counter path %s still resolves to %s", path, pattern)
			}
		}
	}
	if _, err := normalizeScopes([]string{"storage.count"}); err == nil {
		t.Fatal("removed cache service scope is still accepted")
	}
}
