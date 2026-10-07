package controller_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const dashOrigin = "https://dash.example"

type browserFixture struct {
	t      *testing.T
	st     *store.Store
	srv    *controller.Server
	h      http.Handler
	token  string
	sessID string
	csrf   string
}

func newBrowserFixture(t *testing.T, d *controller.Dashboard) *browserFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	token, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("alice", "correct-horse-battery", []string{controller.ScopeAdmin}, now); err != nil {
		t.Fatal(err)
	}
	sessID, csrf, _, err := st.CreateSession(context.Background(), "alice", []string{controller.ScopeAdmin}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := controller.New(st, nil).EnableAuthFromStore()
	if d != nil {
		srv.WithDashboard(*d)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return &browserFixture{t: t, st: st, srv: srv, h: srv.Handler(), token: token, sessID: sessID, csrf: csrf}
}

func (f *browserFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *browserFixture) cookieRequest(method, path, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, dashOrigin+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: f.sessID})
	req.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: f.csrf})
	if method != http.MethodGet {
		req.Header.Set("Origin", dashOrigin)
		req.Header.Set("X-CSRF-Token", f.csrf)
	}
	return req
}

func TestBrowserCookieAuthenticatesTheSessionsPrincipal(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	rec := f.serve(f.cookieRequest(http.MethodGet, "/api/v1/auth/whoami", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"principal":"alice"`) {
		t.Fatalf("whoami with the session cookie = %d %s, want alice", rec.Code, rec.Body)
	}

	anon := httptest.NewRequest(http.MethodGet, dashOrigin+"/api/v1/auth/whoami", nil)
	if rec := f.serve(anon); rec.Code != http.StatusUnauthorized {
		t.Fatalf("whoami without a credential = %d, want 401", rec.Code)
	}
}

func TestBrowserCookieWriteNeedsTheCSRFCheck(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	const path = "/api/v1/secrets/absent"
	for name, spoil := range map[string]func(*http.Request){
		"no header":        func(r *http.Request) { r.Header.Del("X-CSRF-Token") },
		"a forged header":  func(r *http.Request) { r.Header.Set("X-CSRF-Token", "forged") },
		"no origin":        func(r *http.Request) { r.Header.Del("Origin") },
		"another origin":   func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"a planted cookie": func(r *http.Request) { plantCSRF(r, "planted"); r.Header.Set("X-CSRF-Token", "planted") },
	} {
		req := f.cookieRequest(http.MethodDelete, path, "")
		spoil(req)
		if rec := f.serve(req); rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d %q, want 403", name, rec.Code, rec.Body)
		}
	}
	if rec := f.serve(f.cookieRequest(http.MethodDelete, path, "")); rec.Code != http.StatusNotFound {
		t.Fatalf("a checked write = %d %s, want the route's own 404", rec.Code, rec.Body)
	}
}

func plantCSRF(r *http.Request, value string) {
	var kept []string
	for _, c := range r.Cookies() {
		if c.Name != "__Host-sw_csrf" {
			kept = append(kept, c.Name+"="+c.Value)
		}
	}
	r.Header.Set("Cookie", strings.Join(append(kept, "__Host-sw_csrf="+value), "; "))
}

func TestBrowserCredentialHeadersBypassTheCookie(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})

	bearer := f.cookieRequest(http.MethodDelete, "/api/v1/secrets/absent", "")
	bearer.Header.Del("X-CSRF-Token")
	bearer.Header.Del("Origin")
	bearer.Header.Set("Authorization", "Bearer "+f.token)
	if rec := f.serve(bearer); rec.Code != http.StatusNotFound {
		t.Fatalf("bearer write beside a cookie = %d %s, want the route's 404 without a CSRF check", rec.Code, rec.Body)
	}

	wrong := f.cookieRequest(http.MethodGet, "/api/v1/auth/whoami", "")
	wrong.Header.Set("Authorization", "Bearer swu_not-a-token-anyone-minted-000000")
	if rec := f.serve(wrong); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a refused bearer beside a live cookie = %d, want 401 rather than the cookie's principal", rec.Code)
	}

	session := httptest.NewRequest(http.MethodDelete, dashOrigin+"/api/v1/secrets/absent", nil)
	session.Header.Set("Authorization", "Session "+f.sessID)
	if rec := f.serve(session); rec.Code != http.StatusNotFound {
		t.Fatalf("Session-header write = %d %s, want the route's 404 without a CSRF check", rec.Code, rec.Body)
	}
}

func TestBrowserRefusedCookieIsClearedAndAFaultKeepsIt(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	stale := httptest.NewRequest(http.MethodGet, dashOrigin+"/api/v1/auth/whoami", nil)
	stale.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: "swses_unknown"})
	rec := f.serve(stale)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown session = %d, want 401", rec.Code)
	}
	if !clearsCookie(rec, "__Host-sw_session") || !clearsCookie(rec, "__Host-sw_csrf") {
		t.Fatalf("a refused session left its cookies: %v", rec.Header().Values("Set-Cookie"))
	}

	if _, err := f.st.DB().Exec(`DROP TABLE sessions`); err != nil {
		t.Fatal(err)
	}
	rec = f.serve(f.cookieRequest(http.MethodGet, "/api/v1/auth/whoami", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store fault = %d, want 503", rec.Code)
	}
	if len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("store fault touched cookies: %v", rec.Header().Values("Set-Cookie"))
	}
}

func clearsCookie(rec *httptest.ResponseRecorder, name string) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestBrowserInsecureCookiesDropThePrefix(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{InsecureCookies: true})
	prefixed := f.cookieRequest(http.MethodGet, "/api/v1/auth/whoami", "")
	if rec := f.serve(prefixed); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a __Host- cookie on an insecure-cookie dashboard = %d, want 401", rec.Code)
	}
	plain := httptest.NewRequest(http.MethodGet, "http://dash.example/api/v1/auth/whoami", nil)
	plain.AddCookie(&http.Cookie{Name: "sw_session", Value: f.sessID})
	if rec := f.serve(plain); rec.Code != http.StatusOK {
		t.Fatalf("an unprefixed cookie on an insecure-cookie dashboard = %d %s, want 200", rec.Code, rec.Body)
	}

	secure := newBrowserFixture(t, &controller.Dashboard{})
	plain = httptest.NewRequest(http.MethodGet, dashOrigin+"/api/v1/auth/whoami", nil)
	plain.AddCookie(&http.Cookie{Name: "sw_session", Value: secure.sessID})
	if rec := secure.serve(plain); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unprefixed cookie on a secure dashboard = %d, want 401", rec.Code)
	}
}

func TestBrowserCookieIgnoredWithoutASignInSurface(t *testing.T) {
	for name, d := range map[string]*controller.Dashboard{"no dashboard": nil, "local": {Local: true}} {
		f := newBrowserFixture(t, d)
		if rec := f.serve(f.cookieRequest(http.MethodGet, "/api/v1/auth/whoami", "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: whoami by cookie = %d, want 401", name, rec.Code)
		}
	}
}

func TestBrowserSurfaceSecurityHeaders(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	plain := httptest.NewRequest(http.MethodGet, "http://dash.example/api/v1/auth/whoami", nil)
	plain.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: f.sessID})
	rec := f.serve(plain)
	h := rec.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing: %v", h)
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Fatalf("HSTS without TLS evidence: %q", h.Get("Strict-Transport-Security"))
	}

	hsts := newBrowserFixture(t, &controller.Dashboard{HSTS: true})
	plain = httptest.NewRequest(http.MethodGet, "http://dash.example/api/v1/auth/whoami", nil)
	rec = hsts.serve(plain)
	if !strings.HasPrefix(rec.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Fatalf("HSTS dashboard sent no Strict-Transport-Security: %v", rec.Header())
	}
	write := hsts.cookieRequest(http.MethodDelete, "/api/v1/secrets/absent", "")
	write.Header.Set("Origin", "http://dash.example")
	if rec := hsts.serve(write); rec.Code != http.StatusForbidden {
		t.Fatalf("HSTS dashboard accepted an http origin = %d, want 403", rec.Code)
	}
}
