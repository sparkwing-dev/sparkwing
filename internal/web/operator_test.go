package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestOperatorPageServesOnlyTheOperatorsSession(t *testing.T) {
	controllerURL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/session":
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: "someone", CSRFToken: "csrf", UserID: "acct",
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
		case "/api/v1/operator/session":
			if r.Header.Get("Authorization") != "Session operator" {
				w.WriteHeader(http.StatusForbidden)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controllerURL.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controllerURL.URL, RequireLogin: true,
	}, authTestBundle)

	get := func(path, session string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, path := range []string{"/operator", "/operator/", "/operator.html", "/operator.txt"} {
		if code := get(path, "member"); code != http.StatusForbidden {
			t.Errorf("a member's %s = %d want 403", path, code)
		}
	}
	if code := get("/operator", "operator"); code != http.StatusOK {
		t.Errorf("the operator's /operator = %d want 200", code)
	}
	if code := get("/operators-guide", "member"); code != http.StatusOK {
		t.Errorf("a member's /operators-guide = %d want 200", code)
	}
}

func TestOperatorWaitlistApprovalNeedsTheSessionsCSRFToken(t *testing.T) {
	handler, reached := proxyTestDashboard(t, nil)
	const path = "/api/v1/operator/waitlist/approve"
	for name, strip := range map[string]func(*http.Request){
		"no header":      func(r *http.Request) { r.Header.Del(csrfHeaderName) },
		"a forged token": func(r *http.Request) { r.Header.Set(csrfHeaderName, "forged") },
		"another origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
	} {
		req := proxyTestRequest(http.MethodPost, path)
		strip(req)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d want 403", name, rec.Code)
		}
	}
	if got := reached(); len(got) != 0 {
		t.Fatalf("a refused approval reached the controller: %v", got)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, proxyTestRequest(http.MethodPost, path))
	if rec.Code != http.StatusNoContent || !slices.Equal(reached(), []string{"POST " + path}) {
		t.Errorf("with the token = %d, reached %v", rec.Code, reached())
	}
}
