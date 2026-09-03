package controller

import (
	"net/http"
	"testing"
)

func TestAssistedRunHandlerExposesNoUnfencedSecretLogOrMutationSurface(t *testing.T) {
	s := &Server{assistedRunID: "run-1"}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/v1/health", true},
		{http.MethodPost, "/api/v1/nodes/claim/prepare", true},
		{http.MethodPost, "/api/v1/nodes/claim", true},
		{http.MethodGet, "/api/v1/runs/run-1/gitcache/git/repo/info/refs", true},
		{http.MethodGet, "/api/v1/secrets/API_KEY", false},
		{http.MethodPost, "/api/v1/logs/run-1/node-1", false},
		{http.MethodPost, "/api/v1/runs/run-1/nodes/node-1/heartbeat", false},
		{http.MethodPost, "/api/v1/runs/run-1/nodes/node-1/finish", false},
		{http.MethodPost, "/api/v1/runs/run-1/nodes/node-1/finalize-ready", false},
		{http.MethodGet, "/api/v1/runs/other", false},
	} {
		if got := s.assistedPathAllowed(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s allowed = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
