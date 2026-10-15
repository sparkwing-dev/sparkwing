package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheCounterRoutesAreAbsent(t *testing.T) {
	server := &Server{}
	public := server.routers()
	for _, path := range []string{"/internal/storage/reserve", "/internal/storage/commit", "/internal/storage/release", "/internal/downloads/charge", "/internal/egress/totals"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		for _, mux := range []*http.ServeMux{public} {
			if _, pattern := mux.Handler(request); pattern != "" {
				t.Errorf("removed counter path %s still resolves to %s", path, pattern)
			}
		}
	}
}
