package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

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
