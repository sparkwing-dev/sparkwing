package web

import (
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
	handler := SecurityHeaders(false, Pages(testBundle()))
	for _, path := range []string{"/", "/docs", "/_next/static/app.js"} {
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
	for _, tc := range []struct {
		name      string
		hsts      bool
		trusted   bool
		forwarded string
		tls       bool
		want      string
	}{
		{name: "plain HTTP"},
		{name: "TLS listener", tls: true, want: hstsValue},
		{name: "trusted listener forwarded https", trusted: true, forwarded: "https", want: hstsValue},
		{name: "plain listener forwarded https", forwarded: "https"},
		{name: "trusted listener forwarded http", trusted: true, forwarded: "http"},
		{name: "operator asserts TLS", hsts: true, want: hstsValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.1.2.3:9999"
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			h := SecurityHeaders(tc.hsts, Pages(testBundle()))
			if tc.trusted {
				h = ratelimit.TrustedListener(h)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
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
	SecurityHeaders(false, Pages(bundle)).
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

func TestDashboardHTMLNoncesItsInlineScripts(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(false, Pages(testBundle())).
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
