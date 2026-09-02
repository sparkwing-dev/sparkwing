package web

import (
	"context"
	"crypto/tls"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
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
			if got := header.Get("Strict-Transport-Security"); got != "" {
				t.Errorf("Strict-Transport-Security = %q, want none over plain HTTP", got)
			}
		})
	}
}

func TestHSTSNeedsTLSEvidence(t *testing.T) {
	trusted, err := ratelimit.ParseTrustedProxyCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse CIDRs: %v", err)
	}
	for _, tc := range []struct {
		name      string
		opts      HandlerOptions
		peer      string
		forwarded string
		tls       bool
		want      string
	}{
		{name: "plain HTTP", peer: "10.1.2.3:9999"},
		{name: "TLS listener", tls: true, want: hstsValue},
		{
			name:      "trusted forwarded https",
			opts:      HandlerOptions{TrustedProxyCIDRs: trusted},
			peer:      "10.1.2.3:9999",
			forwarded: "https",
			want:      hstsValue,
		},
		{
			name:      "untrusted forwarded https",
			opts:      HandlerOptions{TrustedProxyCIDRs: trusted},
			peer:      "203.0.113.9:9999",
			forwarded: "https",
		},
		{
			name:      "trusted forwarded http",
			opts:      HandlerOptions{TrustedProxyCIDRs: trusted},
			peer:      "10.1.2.3:9999",
			forwarded: "http",
		},
		{
			name:      "forwarded https without trusted CIDRs",
			peer:      "10.1.2.3:9999",
			forwarded: "https",
		},
		{name: "operator asserts TLS", opts: HandlerOptions{HSTS: true}, want: hstsValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.peer != "" {
				req.RemoteAddr = tc.peer
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			HandlerFromOptionsWithBundle(tc.opts, testBundle()).ServeHTTP(rec, req)
			if got := rec.Header().Get("Strict-Transport-Security"); got != tc.want {
				t.Errorf("Strict-Transport-Security = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNonceInlineScriptsCoversTagVariations(t *testing.T) {
	const nonce = "test-nonce"
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "bare", in: "<script>x()</script>", want: `<script nonce="test-nonce">x()</script>`},
		{
			name: "with attribute",
			in:   `<script id="_R_">x()</script>`,
			want: `<script id="_R_" nonce="test-nonce">x()</script>`,
		},
		{name: "upper case", in: "<SCRIPT>x()</SCRIPT>", want: `<SCRIPT nonce="test-nonce">x()</SCRIPT>`},
		{name: "trailing space", in: "<script >x()</script>", want: `<script nonce="test-nonce" >x()</script>`},
		{
			name: "type module",
			in:   `<script type="module" defer>x()</script>`,
			want: `<script type="module" defer nonce="test-nonce">x()</script>`,
		},
		{name: "external", in: `<script src="/a.js"></script>`, want: `<script src="/a.js"></script>`},
		{
			name: "external unquoted",
			in:   `<script defer src=/a.js></script>`,
			want: `<script defer src=/a.js></script>`,
		},
		{name: "angle in attribute", in: `<script data-x="a>b">x()</script>`, want: `<script data-x="a>b" nonce="test-nonce">x()</script>`},
		{name: "not a script tag", in: "<scriptish>x</scriptish>", want: "<scriptish>x</scriptish>"},
		{name: "no tags", in: "plain body", want: "plain body"},
		{name: "unterminated", in: "<script", want: "<script"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(nonceInlineScripts([]byte(tc.in), nonce)); got != tc.want {
				t.Errorf("nonceInlineScripts(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestServedHTMLNoncesEveryInlineScriptShape(t *testing.T) {
	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<html><head><script src="/sparkwing-runtime.js"></script>` +
				`<script id="x">a()</script><SCRIPT>b()</SCRIPT></head><body></body></html>`,
		)},
	}
	rec := httptest.NewRecorder()
	HandlerFromOptionsWithBundle(HandlerOptions{}, bundle).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	_, rest, ok := strings.Cut(rec.Header().Get("Content-Security-Policy"), "'nonce-")
	if !ok {
		t.Fatalf("no script nonce in the policy: %q", rec.Header().Get("Content-Security-Policy"))
	}
	nonce, _, _ := strings.Cut(rest, "'")
	body := rec.Body.String()
	for _, want := range []string{
		`<script id="x" nonce="` + nonce + `">a()`,
		`<SCRIPT nonce="` + nonce + `">b()`,
		`<script src="/sparkwing-runtime.js">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("served HTML missing %q: %s", want, body)
		}
	}
}

func TestServeWithOptionsRefusesRemoteTokenBind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ServeWithOptions(ctx, HandlerOptions{Token: "service-token"}, "0.0.0.0:0")
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("ServeWithOptions error = %v, want the non-loopback refusal", err)
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

func TestValidateCookieExposure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    HandlerOptions
		addr    string
		wantErr bool
	}{
		{name: "secure cookies anywhere", addr: "0.0.0.0:4343"},
		{
			name: "insecure on loopback",
			opts: HandlerOptions{InsecureCookies: true},
			addr: "127.0.0.1:4343",
		},
		{
			name: "insecure on localhost",
			opts: HandlerOptions{InsecureCookies: true},
			addr: "localhost:4343",
		},
		{
			name: "insecure on IPv6 loopback",
			opts: HandlerOptions{InsecureCookies: true},
			addr: "[::1]:4343",
		},
		{
			name: "insecure remote with opt-in",
			opts: HandlerOptions{InsecureCookies: true, AllowInsecureCookiesRemote: true},
			addr: "0.0.0.0:4343",
		},
		{
			name:    "insecure on wildcard bind",
			opts:    HandlerOptions{InsecureCookies: true},
			addr:    "0.0.0.0:4343",
			wantErr: true,
		},
		{
			name:    "insecure on routable bind",
			opts:    HandlerOptions{InsecureCookies: true},
			addr:    "10.0.0.5:4343",
			wantErr: true,
		},
		{
			name:    "insecure on port only",
			opts:    HandlerOptions{InsecureCookies: true},
			addr:    ":4343",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCookieExposure(tc.opts, tc.addr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateCookieExposure(%q) error = %v, want error %v", tc.addr, err, tc.wantErr)
			}
		})
	}
}

func TestServeRejectsInsecureCookiesOnNonLoopbackBind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := ServeWithOptions(ctx, HandlerOptions{InsecureCookies: true}, "0.0.0.0:0")
	if err == nil || !strings.Contains(err.Error(), "insecure cookies") {
		t.Fatalf("ServeWithOptions error = %v, want an insecure-cookie refusal", err)
	}
}
