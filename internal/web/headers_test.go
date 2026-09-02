package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testBundle() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<html><head><script src="/sparkwing-runtime.js"></script>` +
				`<script>self.__next_f.push([0])</script></head><body></body></html>`,
		)},
		"_next/static/app.js": &fstest.MapFile{Data: []byte("export {};")},
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	handler := HandlerFromOptionsWithBundle(HandlerOptions{Version: "v1.2.3"}, testBundle())
	for _, path := range []string{"/", "/api/health", "/sparkwing-runtime.js", "/_next/static/app.js"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			header := rec.Header()
			csp := header.Get("Content-Security-Policy")
			for _, directive := range []string{
				"default-src 'self'",
				"frame-ancestors 'none'",
				"object-src 'none'",
				"script-src 'self' 'nonce-",
			} {
				if !strings.Contains(csp, directive) {
					t.Errorf("Content-Security-Policy = %q, want %q", csp, directive)
				}
			}
			if got := header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q, want DENY", got)
			}
			if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := header.Get("Referrer-Policy"); got != "same-origin" {
				t.Errorf("Referrer-Policy = %q, want same-origin", got)
			}
			if got := header.Get("Strict-Transport-Security"); got != hstsValue {
				t.Errorf("Strict-Transport-Security = %q, want %q", got, hstsValue)
			}
		})
	}
}

func TestSecurityHeadersDropHSTSWithInsecureCookies(t *testing.T) {
	restore := cookieSecure
	cookieSecure = false
	t.Cleanup(func() { cookieSecure = restore })

	rec := httptest.NewRecorder()
	HandlerFromOptionsWithBundle(HandlerOptions{}, testBundle()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q, want none when cookies are not Secure", got)
	}
}

func TestDashboardHTMLNoncesItsInlineScripts(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlerFromOptionsWithBundle(HandlerOptions{}, testBundle()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	_, rest, ok := strings.Cut(csp, "'nonce-")
	if !ok {
		t.Fatalf("Content-Security-Policy = %q, want a script nonce", csp)
	}
	nonce, _, _ := strings.Cut(rest, "'")
	if nonce == "" {
		t.Fatalf("Content-Security-Policy = %q, want a non-empty script nonce", csp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<script nonce="`+nonce+`">self.__next_f.push`) {
		t.Errorf("inline script missing nonce %q: %s", nonce, body)
	}
	if strings.Contains(body, `<script nonce="`+nonce+`" src=`) {
		t.Errorf("nonce leaked onto an external script: %s", body)
	}
}

func TestNoBearerReachesTheBrowser(t *testing.T) {
	for _, requireLogin := range []bool{false, true} {
		opts := HandlerOptions{
			Token:             "service-token",
			ControllerURL:     "http://controller.test",
			AuthControllerURL: "http://controller.test",
			RequireLogin:      requireLogin,
		}
		handler := HandlerFromOptionsWithBundle(opts, testBundle())
		for _, path := range []string{"/", "/sparkwing-runtime.js"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if strings.Contains(rec.Body.String(), "service-token") {
				t.Errorf("require-login=%v %s exposed the service token: %s",
					requireLogin, path, rec.Body.String())
			}
		}
	}
}

func TestRuntimeConfigCannotBreakOutOfItsScript(t *testing.T) {
	hostile := "</script><script>alert(1)</script>\u2028\u2029"
	rec := httptest.NewRecorder()
	runtimeConfigHandler(HandlerOptions{Version: hostile})(
		rec, httptest.NewRequest(http.MethodGet, runtimeConfigPath, nil))

	body := rec.Body.String()
	for _, forbidden := range []string{"</script", "<script", "\u2028", "\u2029"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("runtime config left %q unescaped: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `\u003c/script\u003e`) {
		t.Errorf("runtime config lost the escaped payload: %s", body)
	}
}

func TestJSStringLiteralEscapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "v1.2.3", want: `"v1.2.3"`},
		{name: "script close", in: "</script>", want: `"\u003c/script\u003e"`},
		{name: "ampersand", in: "a&b", want: `"a\u0026b"`},
		{name: "line separator", in: "a\u2028b", want: `"a\u2028b"`},
		{name: "paragraph separator", in: "a\u2029b", want: `"a\u2029b"`},
		{name: "quote", in: `a"b`, want: `"a\"b"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsStringLiteral(tc.in); got != tc.want {
				t.Errorf("jsStringLiteral(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateRemoteExposure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    HandlerOptions
		addr    string
		wantErr bool
	}{
		{name: "loopback with token", opts: HandlerOptions{Token: "t"}, addr: "127.0.0.1:4343"},
		{name: "localhost with token", opts: HandlerOptions{Token: "t"}, addr: "localhost:4343"},
		{name: "ipv6 loopback with token", opts: HandlerOptions{Token: "t"}, addr: "[::1]:4343"},
		{name: "remote without token", addr: "0.0.0.0:4343"},
		{
			name: "remote token with login",
			opts: HandlerOptions{Token: "t", RequireLogin: true},
			addr: "0.0.0.0:4343",
		},
		{
			name: "remote token with opt-in",
			opts: HandlerOptions{Token: "t", AllowUnauthenticatedRemote: true},
			addr: "0.0.0.0:4343",
		},
		{
			name:    "wildcard bind",
			opts:    HandlerOptions{Token: "t"},
			addr:    "0.0.0.0:4343",
			wantErr: true,
		},
		{
			name:    "routable bind",
			opts:    HandlerOptions{Token: "t"},
			addr:    "10.0.0.5:4343",
			wantErr: true,
		},
		{
			name:    "port only",
			opts:    HandlerOptions{Token: "t"},
			addr:    ":4343",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRemoteExposure(tc.opts, tc.addr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRemoteExposure(%q) error = %v, want error %v", tc.addr, err, tc.wantErr)
			}
		})
	}
}
