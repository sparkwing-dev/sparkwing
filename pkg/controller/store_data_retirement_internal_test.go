package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func FuzzLegacyStorageCountersAreGone(f *testing.F) {
	f.Add("", "first", int64(1))
	f.Add("Bearer cache-token", "second", int64(-1))
	f.Add("Bearer admin", "default", int64(1<<40))
	f.Fuzz(func(t *testing.T, auth, team string, size int64) {
		handler := New(nil, nil).Handler()
		body, err := json.Marshal(map[string]any{"team": team, "store": "cache", "reservation": "r", "bytes": size, "next_bytes": size})
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/internal/storage/reserve", "/internal/storage/commit", "/internal/storage/release", "/internal/downloads/charge"} {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
			request.Header.Set("Authorization", auth)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired route %s: %d %s", path, response.Code, response.Body.String())
			}
		}
	})
}

func TestLegacyEgressTotalsEndpointIsGone(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/internal/egress/totals", strings.NewReader(`{"service":"cache","day":"2026-10-14","month":"2026-10"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(nil, nil).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed egress endpoint: %d %s", response.Code, response.Body.String())
	}
}
