package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
)

// profileDashboard serves the operator's own store and asks the controller
// only whether a session is live, the way --profile and --state run.
func profileDashboard(t *testing.T, controllerURL string) http.Handler {
	t.Helper()
	return HandlerFromOptionsWithBundle(HandlerOptions{
		Backend:           &fakeBackend{caps: backend.Capabilities{Mode: "cluster"}},
		AuthControllerURL: controllerURL,
		RequireLogin:      true,
	}, authTestBundle)
}

// A dashboard that reads the operator's store directly would show a signed-up
// account every team's runs, so it serves no account session and offers no
// account sign-in. The operator's own session still works there, and an
// account session still works on a dashboard that forwards every read.
func TestProfileModeDashboardServesNoAccountSession(t *testing.T) {
	t.Parallel()
	account := newIdentityController(t, true)
	account.account = true
	handler := profileDashboard(t, account.URL)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedInRequest(http.MethodGet, "/api/v1/runs", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an account session reading runs on a profile dashboard = %d, want 401: %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://dashboard.example/login", nil))
	if body := rec.Body.String(); strings.Contains(body, "Sign in with Google") || strings.Contains(body, "Sign in with GitHub") {
		t.Errorf("a profile dashboard offers account sign-in: %s", body)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://dashboard.example/auth/google/start", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("account sign-in start on a profile dashboard = %d, want 404", rec.Code)
	}
	if starts, _, _, _ := account.snapshot(); len(starts) != 0 {
		t.Errorf("a profile dashboard started account sign-in: %v", starts)
	}

	operator := newIdentityController(t, true)
	rec = httptest.NewRecorder()
	profileDashboard(t, operator.URL).ServeHTTP(rec, signedInRequest(http.MethodGet, "/api/v1/runs", ""))
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("the operator's session on a profile dashboard = 401: %s", rec.Body)
	}

	forwarded := newIdentityController(t, true)
	forwarded.account = true
	rec = httptest.NewRecorder()
	teamDashboard(t, forwarded.URL).ServeHTTP(rec, signedInRequest(http.MethodGet, "/api/v1/runs", ""))
	if rec.Code != http.StatusNoContent {
		t.Errorf("an account session on a --controller dashboard = %d, want 204: %s", rec.Code, rec.Body)
	}
}

func TestServiceHealthNeedsARole(t *testing.T) {
	t.Parallel()
	handler := healthServicesHandler([]HealthService{{Name: "cache", URL: "http://cache.internal:8080/health"}}, "")
	roleless := httptest.NewRequest(http.MethodGet, "/api/v1/health/services", nil)
	roleless = roleless.WithContext(contextWithWebPrincipal(roleless.Context(), &sessionResp{Principal: "ada"}, "s"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, roleless)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "cache.internal") {
		t.Fatalf("a roleless session = %d %s, want 403 naming no service", rec.Code, rec.Body)
	}
	member := httptest.NewRequest(http.MethodGet, "/api/v1/health/services", nil)
	member = member.WithContext(contextWithWebPrincipal(member.Context(),
		&sessionResp{Principal: "ada", Scopes: []string{"runs.read"}}, "s"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("a member's session = %d, want 200", rec.Code)
	}
}

func TestOAuthStartSendsTheBrowsersAddress(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	req := httptest.NewRequest(http.MethodGet, "https://dashboard.example/auth/google/start", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	teamDashboard(t, ctrl.URL).ServeHTTP(httptest.NewRecorder(), req)
	starts, _, _, _ := ctrl.snapshot()
	if len(starts) != 1 || starts[0]["x-forwarded-for"] != "203.0.113.7" {
		t.Fatalf("starts = %v, want one carrying the browser's address", starts)
	}
}

func TestOAuthSignInEndsTheBrowsersEarlierSession(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	req := httptest.NewRequest(http.MethodGet,
		"https://dashboard.example/auth/google/callback?code=real-code&state="+fakeOAuthState, nil)
	req.AddCookie(&http.Cookie{Name: "__Host-sw_oauth", Value: providerFlowCookie("google", fakeOAuthState, fakeVerifier, "/")})
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "earlier-session"})
	rec := httptest.NewRecorder()
	teamDashboard(t, ctrl.URL).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d: %s", rec.Code, rec.Body)
	}
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if len(ctrl.logouts) != 1 || ctrl.logouts[0] != "earlier-session" {
		t.Fatalf("logouts = %v, want the earlier session ended", ctrl.logouts)
	}
}
