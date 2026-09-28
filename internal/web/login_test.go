package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

func TestLoginFormSurvivesAnotherLoginPageLoad(t *testing.T) {
	t.Parallel()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/bootstrap-needed":
			_, _ = w.Write([]byte(`{"needed":false}`))
		case "/api/v1/auth/login":
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle)

	first := httptest.NewRequest(http.MethodGet, "https://dashboard.example/login", nil)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	firstBody, err := io.ReadAll(firstResponse.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	firstResponse.Result().Body.Close()
	firstCSRF := loginFormCSRF(t, firstBody)
	firstCookie := firstResponse.Result().Cookies()[0]

	second := httptest.NewRequest(http.MethodGet, "https://dashboard.example/login", nil)
	second.AddCookie(firstCookie)
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, second)
	secondCookie := secondResponse.Result().Cookies()[0]

	form := url.Values{
		"username":   {"invalid-user"},
		"password":   {"invalid-password"},
		"csrf_token": {firstCSRF},
	}
	post := httptest.NewRequest(http.MethodPost, "https://dashboard.example/login", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "https://dashboard.example")
	post.AddCookie(secondCookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, post)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("login with an earlier form after another page load = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func loginFormCSRF(t *testing.T, body []byte) string {
	t.Helper()
	const marker = `name="csrf_token" value="`
	start := strings.Index(string(body), marker)
	if start < 0 {
		t.Fatal("login form has no CSRF field")
	}
	valueStart := start + len(marker)
	valueEnd := strings.IndexByte(string(body[valueStart:]), '"')
	if valueEnd < 0 {
		t.Fatal("login form CSRF field has no closing quote")
	}
	return string(body[valueStart : valueStart+valueEnd])
}

func TestSafeNext(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/runs", "/runs"},
		{"/pipelines/foo?x=1", "/pipelines/foo?x=1"},
		{"/runs?run=x&tab=logs", "/runs?run=x&tab=logs"},
		{"//evil.com/foo", "/"},
		{"//evil.com", "/"},
		{`/\evil.com`, "/"},
		{`/safe\evil.com`, "/"},
		{"/%2f%2fevil.com", "/"},
		{"/%5cevil.com", "/"},
		{"/runs#fragment", "/"},
		{"/runs\nmalformed", "/"},
		{"https://evil.com", "/"},
		{"http://evil.com", "/"},
		{"javascript:alert(1)", "/"},
		{"runs", "/"},
		{"../etc", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := safeNext(tc.in); got != tc.want {
				t.Fatalf("safeNext(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}

var relayCases = []struct {
	name       string
	trusted    bool
	remoteAddr string
	realIP     string
	want       string
}{
	{name: "plain listener relays the peer over a spoofed address", remoteAddr: "198.51.100.9:4444", realIP: "203.0.113.11", want: "198.51.100.9"},
	{name: "trusted listener relays the forwarded address", trusted: true, remoteAddr: "10.1.2.3:4444", realIP: "203.0.113.11", want: "203.0.113.11"},
	{name: "trusted listener without the header relays the peer", trusted: true, remoteAddr: "10.1.2.3:4444", want: "10.1.2.3"},
}

func relayRecorder(t *testing.T, path string, reply func(http.ResponseWriter)) (*httptest.Server, chan string) {
	t.Helper()
	seen := make(chan string, 4)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			seen <- strings.Join(r.Header.Values("X-Real-IP"), ",")
			reply(w)
			return
		}
		http.Error(w, "unexpected", http.StatusTeapot)
	}))
	t.Cleanup(controller.Close)
	return controller, seen
}

func serveRelayCase(t *testing.T, opts HandlerOptions, req *http.Request, trusted bool, realIP string) {
	t.Helper()
	handler := HandlerFromOptionsWithBundle(opts, authTestBundle)
	if trusted {
		handler = ratelimit.TrustedListener(handler)
	}
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func expectRelayed(t *testing.T, seen chan string, want string) {
	t.Helper()
	select {
	case got := <-seen:
		if got != want {
			t.Fatalf("upstream saw X-Real-IP %q, want %q", got, want)
		}
	default:
		t.Fatalf("upstream never saw the request")
	}
}

func TestLoginRelaysVerifiedBrowserAddressToController(t *testing.T) {
	t.Parallel()
	controller, seen := relayRecorder(t, "/api/v1/auth/login", func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(loginResp{
			SessionID: "sid", CSRFToken: "csrf", Principal: "admin",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
	})
	for _, tc := range relayCases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{
				"username":   {"admin"},
				"password":   {"correct-horse"},
				"csrf_token": {"tok"},
			}
			req := httptest.NewRequest(http.MethodPost, "https://dashboard.example/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "https://dashboard.example")
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "tok"})
			req.RemoteAddr = tc.remoteAddr
			serveRelayCase(t, HandlerOptions{ControllerURL: controller.URL, RequireLogin: true}, req, tc.trusted, tc.realIP)
			expectRelayed(t, seen, tc.want)
		})
	}
}

func TestProxiedAPICallRelaysVerifiedBrowserAddress(t *testing.T) {
	t.Parallel()
	controller, seen := relayRecorder(t, "/api/v1/runs", func(w http.ResponseWriter) {
		_, _ = w.Write([]byte("[]"))
	})
	for _, tc := range relayCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://dashboard.example/api/v1/runs", nil)
			req.RemoteAddr = tc.remoteAddr
			serveRelayCase(t, HandlerOptions{ControllerURL: controller.URL, Token: "service-token"}, req, tc.trusted, tc.realIP)
			expectRelayed(t, seen, tc.want)
		})
	}
}

func TestLogsProxyNeverRelaysAnAddress(t *testing.T) {
	t.Parallel()
	logs, seen := relayRecorder(t, "/api/v1/logs/run-1/node-1", func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, tc := range relayCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://dashboard.example/api/v1/logs/run-1/node-1", nil)
			req.RemoteAddr = tc.remoteAddr
			serveRelayCase(t, HandlerOptions{ControllerURL: logs.URL, LogsURL: logs.URL, Token: "service-token"}, req, tc.trusted, tc.realIP)
			expectRelayed(t, seen, "")
		})
	}
}

func TestDashboardSessionCookiesPersistAcrossBrowserRestarts(t *testing.T) {
	t.Parallel()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(loginResp{
				SessionID: "session", CSRFToken: "csrf", Principal: "admin",
				ExpiresAt: time.Now().Add(7 * 24 * time.Hour).Unix(),
			})
		case "/api/v1/auth/session":
			if r.Header.Get("Authorization") != "Session session" {
				http.Error(w, "invalid session", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: "admin", CSRFToken: "csrf",
				ExpiresAt: time.Now().Add(7 * 24 * time.Hour).Unix(),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle)

	form := url.Values{"username": {"admin"}, "password": {"correct-horse"}, "csrf_token": {"tok"}}
	login := httptest.NewRequest(http.MethodPost, "https://dashboard.example/login", strings.NewReader(form.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Origin", "https://dashboard.example")
	login.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "tok"})
	issued := httptest.NewRecorder()
	handler.ServeHTTP(issued, login)
	if issued.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, want redirect", issued.Code)
	}
	assertPersistentAuthCookies(t, issued.Result().Cookies())

	reopened := httptest.NewRequest(http.MethodGet, "https://dashboard.example/", nil)
	addAuthCookies(t, reopened, issued.Result().Cookies())
	active := httptest.NewRecorder()
	handler.ServeHTTP(active, reopened)
	if active.Code != http.StatusOK {
		t.Fatalf("authenticated page after browser restart = %d, want 200", active.Code)
	}
}

func assertPersistentAuthCookies(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		cookie := findCookie(cookies, name)
		if cookie == nil {
			t.Fatalf("%s was not refreshed", name)
		}
		wantSameSite := http.SameSiteStrictMode
		if name == sessionCookieName {
			wantSameSite = http.SameSiteLaxMode
		}
		if cookie.MaxAge != int(30*24*time.Hour/time.Second) || !cookie.Secure || cookie.SameSite != wantSameSite || cookie.Path != "/" || cookie.Domain != "" {
			t.Errorf("%s persistence or security attributes = MaxAge %d, Secure %t, SameSite %d, Path %q, Domain %q", name, cookie.MaxAge, cookie.Secure, cookie.SameSite, cookie.Path, cookie.Domain)
		}
		if name == sessionCookieName && !cookie.HttpOnly {
			t.Error("session cookie is not HttpOnly")
		}
	}
}
