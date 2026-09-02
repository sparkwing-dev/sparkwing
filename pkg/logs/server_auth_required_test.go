package logs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newHealthServer(t *testing.T, controllerURL string) *Server {
	t.Helper()
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if controllerURL != "" {
		s.WithControllerAuth(controllerURL, 0)
	}
	return s
}

func healthAuthField(t *testing.T, s *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200 -- body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string `json:"status"`
		Auth   string `json:"auth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health body: %v -- raw=%q", err, rec.Body.String())
	}
	return body.Auth
}

func TestHealthReportsWhetherTokensAreResolved(t *testing.T) {
	for _, tc := range []struct {
		name          string
		controllerURL string
		want          string
	}{
		{name: "no controller", controllerURL: "", want: "disabled"},
		{name: "controller wired", controllerURL: "https://controller.example.com", want: "enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthAuthField(t, newHealthServer(t, tc.controllerURL)); got != tc.want {
				t.Errorf("health auth = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWhoamiRejectsAnUnauthenticatedControllersAnonymousPrincipal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantAllow bool
	}{
		{name: "kind none", body: `{"principal":"unauthed","kind":"none"}`},
		{name: "unauthed name", body: `{"principal":"unauthed","kind":"user","scopes":["admin"]}`},
		{name: "kind none with a real name", body: `{"principal":"runner-1","kind":"none","scopes":["admin"]}`},
		{
			name:      "real principal",
			body:      `{"principal":"runner-1","kind":"runner","scopes":["logs.read"]}`,
			wantAllow: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer controller.Close()

			s := newHealthServer(t, controller.URL)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/run-1/step-a", nil)
			req.Header.Set("Authorization", "Bearer swu_whatever")
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)

			if tc.wantAllow {
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 for a real principal -- body %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 -- body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAnonymousPrincipalIsNotCached(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"principal":"unauthed","kind":"none"}`))
	}))
	defer controller.Close()

	s := newHealthServer(t, controller.URL)
	s.authCacheTTL = time.Minute
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/run-1/step-a", nil)
	req.Header.Set("Authorization", "Bearer swu_whatever")
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if _, cached := s.authCache.Load("swu_whatever"); cached {
		t.Error("the controller's anonymous principal was cached and would be trusted for the TTL")
	}
}
