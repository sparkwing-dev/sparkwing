package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type hostPrefixController struct {
	mu       sync.Mutex
	resolved []string
}

func (c *hostPrefixController) record(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolved = append(c.resolved, id)
}

func (c *hostPrefixController) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.resolved...)
}

func newHostPrefixDashboard(t *testing.T, insecure bool) (*hostPrefixController, http.Handler) {
	t.Helper()
	calls := &hostPrefixController{}
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/bootstrap-needed":
			_ = json.NewEncoder(w).Encode(map[string]bool{"needed": false})
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id": "victim-session",
				"csrf_token": "victim-token",
				"principal":  "victim",
				"scopes":     []string{"admin"},
				"expires_at": time.Now().Add(time.Hour).Unix(),
			})
		case "/api/v1/auth/session":
			id := strings.TrimPrefix(r.Header.Get("Authorization"), "Session ")
			calls.record(id)
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: id,
				Scopes:    []string{"admin"},
				CSRFToken: id + "-token",
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(controller.Close)
	return calls, HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL:   controller.URL,
		RequireLogin:    true,
		InsecureCookies: insecure,
	}, authTestBundle)
}

// A sibling host under the registrable domain can only write the unprefixed
// name, because a browser refuses a __Host- cookie that carries Domain. The
// same value under the prefixed name is accepted, so the refusal is the name
// and nothing else about the request. The controller stub resolves any session
// id and records it, so the assertions name the cookie the dashboard read.
func TestPlantedUnprefixedSessionCookieIsRefused(t *testing.T) {
	t.Parallel()
	calls, dashboard := newHostPrefixDashboard(t, false)

	planted := httptest.NewRequest(http.MethodGet, "https://dashboard.example.com/", nil)
	planted.AddCookie(&http.Cookie{Name: "sw_session", Value: "attacker-session"})
	rec := httptest.NewRecorder()
	dashboard.ServeHTTP(rec, planted)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("planted sw_session = %d, want 303 to the sign-in page", rec.Code)
	}
	if location := rec.Header().Get("Location"); !strings.HasPrefix(location, "/login") {
		t.Fatalf("planted sw_session redirected to %q, want /login", location)
	}
	if seen := calls.seen(); len(seen) != 0 {
		t.Fatalf("planted sw_session reached the controller as %v", seen)
	}

	prefixed := httptest.NewRequest(http.MethodGet, "https://dashboard.example.com/", nil)
	prefixed.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: "attacker-session"})
	rec = httptest.NewRecorder()
	dashboard.ServeHTTP(rec, prefixed)
	if rec.Code != http.StatusOK {
		t.Fatalf("__Host-sw_session with the same value = %d, want 200", rec.Code)
	}
	if seen := calls.seen(); len(seen) != 1 || seen[0] != "attacker-session" {
		t.Fatalf("controller resolved %v, want one attacker-session", seen)
	}
}

// A longer Path sorts a planted cookie ahead of the browser's own in the
// Cookie header, and r.Cookie returns the first duplicate, so the first
// position is the one the plant buys and the one both read sites must survive.
func TestPlantedCookieSentFirstDoesNotShadowTheRealSession(t *testing.T) {
	t.Parallel()
	calls, dashboard := newHostPrefixDashboard(t, false)

	req := httptest.NewRequest(http.MethodPost, "https://dashboard.example.com/api/v1/runs/run-1/cancel", nil)
	req.Header.Set("Origin", "https://dashboard.example.com")
	req.Header.Set(csrfHeaderName, "victim-session-token")
	req.AddCookie(&http.Cookie{Name: "sw_session", Value: "attacker-session"})
	req.AddCookie(&http.Cookie{Name: "sw_csrf", Value: "attacker-session-token"})
	req.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: "victim-session"})
	req.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: "victim-session-token"})

	rec := httptest.NewRecorder()
	dashboard.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel with a planted cookie in front = %d, want 200: %s", rec.Code, rec.Body)
	}
	if seen := calls.seen(); len(seen) != 1 || seen[0] != "victim-session" {
		t.Fatalf("controller resolved %v, want one victim-session", seen)
	}
}

// The local-http escape drops Secure, and a browser refuses a __Host- cookie
// without it, so the escape has to drop the prefix from both names or sign
// nobody in. It doubles as the negative control: with the prefix out of play
// the plant from the first test is the cookie the dashboard reads.
func TestInsecureCookieEscapeDropsThePrefixEndToEnd(t *testing.T) {
	t.Parallel()
	calls, dashboard := newHostPrefixDashboard(t, true)

	page := httptest.NewRequest(http.MethodGet, "http://localhost:8080/login", nil)
	rec := httptest.NewRecorder()
	dashboard.ServeHTTP(rec, page)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", rec.Code)
	}
	formToken := cookieByName(t, rec.Result().Cookies(), "sw_csrf")

	form := url.Values{"username": {"victim"}, "password": {"correct-horse"}, "csrf_token": {formToken}}
	submit := httptest.NewRequest(http.MethodPost, "http://localhost:8080/login", strings.NewReader(form.Encode()))
	submit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	submit.Header.Set("Origin", "http://localhost:8080")
	submit.AddCookie(&http.Cookie{Name: "sw_csrf", Value: formToken})
	rec = httptest.NewRecorder()
	dashboard.ServeHTTP(rec, submit)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303: %s", rec.Code, rec.Body)
	}
	issued := rec.Result().Cookies()
	for _, c := range issued {
		if strings.HasPrefix(c.Name, hostPrefix) {
			t.Fatalf("insecure deployment issued %q, which a browser without Secure drops", c.Name)
		}
	}
	session := cookieByName(t, issued, "sw_session")
	if cookieByName(t, issued, "sw_csrf") == "" {
		t.Fatal("insecure deployment issued no sw_csrf cookie")
	}

	dashboardPage := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	dashboardPage.AddCookie(&http.Cookie{Name: "sw_session", Value: session})
	rec = httptest.NewRecorder()
	dashboard.ServeHTTP(rec, dashboardPage)
	if rec.Code != http.StatusOK {
		t.Fatalf("unprefixed session on an insecure deployment = %d, want 200", rec.Code)
	}
	if seen := calls.seen(); len(seen) != 1 || seen[0] != "victim-session" {
		t.Fatalf("controller resolved %v, want one victim-session", seen)
	}
}

func cookieByName(t *testing.T, cookies []*http.Cookie, name string) string {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("response set no %s cookie", name)
	return ""
}
