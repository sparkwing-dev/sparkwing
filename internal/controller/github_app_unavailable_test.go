package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestGitHubAppUnavailableKeepsControllerAvailable(t *testing.T) {
	s, session := credentialSessionServer(t)
	s.WithGitHubAppUnavailable(errors.New("private key is malformed"))
	for _, tc := range []struct {
		method, path, want string
		status             int
	}{
		{"GET", "/api/v1/health", `"status":"degraded"`, http.StatusOK},
		{"GET", "/api/v1/team/github-app", "GitHub App unavailable: private key is malformed", http.StatusServiceUnavailable},
		{"POST", "/webhooks/github-app", "GitHub App unavailable: private key is malformed", http.StatusServiceUnavailable},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) || !strings.Contains(w.Body.String(), "GitHub App unavailable: private key is malformed") {
			t.Fatalf("%s: HTTP %d %s", tc.path, w.Code, w.Body)
		}
	}
	provider := s.sourceAdapter().GitHub
	if provider == nil {
		t.Fatal("App source provider is missing")
	}
	if _, err := provider.InstallationTokenByIDs(t.Context(), 7, []int64{701}, nil); err == nil || !strings.Contains(err.Error(), "GitHub App unavailable: private key is malformed") {
		t.Fatalf("App source token: %v", err)
	}
	if _, err := s.ResolveScheduleSource(t.Context(), store.CronSchedule{GitHubInstallationID: 7}, runspec.Request{}); err == nil || !strings.Contains(err.Error(), "GitHub App unavailable: private key is malformed") {
		t.Fatalf("App schedule: %v", err)
	}
}
