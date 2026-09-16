package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
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

func TestLoginForwardsBrowserAddressToController(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 4)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			seen <- r.Header.Get("X-Forwarded-For")
			_ = json.NewEncoder(w).Encode(loginResp{
				SessionID: "sid", CSRFToken: "csrf", Principal: "admin",
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
			return
		}
		http.Error(w, "unexpected", http.StatusTeapot)
	}))
	t.Cleanup(controller.Close)

	cases := []struct {
		name       string
		trusted    []netip.Prefix
		remoteAddr string
		forwarded  string
		want       string
	}{
		{
			name:       "peer address",
			remoteAddr: "198.51.100.9:4444",
			want:       "198.51.100.9",
		},
		{
			name:       "browser behind a trusted proxy",
			trusted:    []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
			remoteAddr: "10.1.2.3:4444",
			forwarded:  "203.0.113.11",
			want:       "203.0.113.11",
		},
		{
			name:       "untrusted peer cannot forge",
			remoteAddr: "198.51.100.9:4444",
			forwarded:  "203.0.113.11",
			want:       "198.51.100.9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := HandlerFromOptionsWithBundle(HandlerOptions{
				ControllerURL:     controller.URL,
				RequireLogin:      true,
				TrustedProxyCIDRs: tc.trusted,
			}, authTestBundle)

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
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			handler.ServeHTTP(httptest.NewRecorder(), req)

			select {
			case got := <-seen:
				if got != tc.want {
					t.Fatalf("X-Forwarded-For = %q, want %q", got, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("controller never saw a login")
			}
		})
	}
}
