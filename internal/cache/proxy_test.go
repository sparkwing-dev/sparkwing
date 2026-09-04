package cache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func expireProxyCacheEntry(t *testing.T, registry, path string) {
	t.Helper()
	metaPath := filepath.Join(proxyDir, registry, proxyCacheKey(registry, path)+".meta")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta proxyMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatal(err)
	}
	meta.CachedAt = 0
	metaData, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, metaData, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProxyCacheKey_Deterministic(t *testing.T) {
	k1 := proxyCacheKey("npm", "lodash/-/lodash-4.17.21.tgz")
	k2 := proxyCacheKey("npm", "lodash/-/lodash-4.17.21.tgz")
	if k1 != k2 {
		t.Error("same input should produce same key")
	}
	if len(k1) != 16 {
		t.Errorf("expected 16 char key, got %d", len(k1))
	}
}

func TestProxyCacheKey_Different(t *testing.T) {
	k1 := proxyCacheKey("npm", "lodash/-/lodash-4.17.21.tgz")
	k2 := proxyCacheKey("npm", "express/-/express-4.18.2.tgz")
	if k1 == k2 {
		t.Error("different paths should produce different keys")
	}

	k3 := proxyCacheKey("pypi", "lodash/-/lodash-4.17.21.tgz")
	if k1 == k3 {
		t.Error("different registries should produce different keys")
	}
}

func TestIsImmutable(t *testing.T) {
	immutable := []string{
		"lodash/-/lodash-4.17.21.tgz",
		"packages/requests-2.31.0.tar.gz",
		"numpy-1.24.0-cp311-cp311-linux_x86_64.whl",
		"rails-7.1.0.gem",
		"some-package-1.0.0.zip",
		"guava-32.1.jar",
		"tokio-1.35.0.crate",
		"alpine/v3.21/main/x86_64/git-2.43.0-r0.apk",
	}
	for _, p := range immutable {
		if !isImmutable(p) {
			t.Errorf("expected %q to be immutable", p)
		}
	}

	mutable := []string{
		"lodash",
		"simple/requests/",
		"api/v1/dependencies",
		"@types/node",
		"info/refs",
	}
	for _, p := range mutable {
		if isImmutable(p) {
			t.Errorf("expected %q to be mutable", p)
		}
	}
}

func TestHandleProxy_UnknownRegistry(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/proxy/foobar/something", nil)
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for unknown registry, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unknown registry") {
		t.Errorf("expected error about unknown registry, got: %s", w.Body.String())
	}
}

func TestHandleProxy_PathTraversal(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/proxy/npm/../../etc/passwd", nil)
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for path traversal, got %d", w.Code)
	}
}

func TestHandleProxy_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/proxy/npm/lodash", nil)
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405 for POST, got %d", w.Code)
	}
}

func TestHandleProxy_MissingRegistry(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/proxy/", nil)
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func withTestProxy(t *testing.T, registries map[string]Registry, fn func()) {
	t.Helper()
	oldRegistries := defaultRegistries
	oldProxyDir := proxyDir
	oldPublicBase := proxyPublicBase
	oldTrustForwarded := proxyTrustForwardedHost
	defaultRegistries = registries
	proxyDir = t.TempDir()
	proxyPublicBase = ""
	proxyTrustForwardedHost = false
	for name := range registries {
		os.MkdirAll(filepath.Join(proxyDir, name), 0o755)
	}
	defer func() {
		defaultRegistries = oldRegistries
		proxyDir = oldProxyDir
		proxyPublicBase = oldPublicBase
		proxyTrustForwardedHost = oldTrustForwarded
		// safety: the shared client parks an idle keep-alive connection per upstream,
		// and a real registry holds one open long after the test that reached it.
		proxyClient.CloseIdleConnections()
	}()
	fn()
}

func TestHandleProxy_CacheMissAndHit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"testpkg","version":"1.0.0"}`))
	}))
	defer upstream.Close()

	withTestProxy(t, map[string]Registry{
		"test": {Name: "test", Upstream: upstream.URL, RewriteBody: false},
	}, func() {
		req1 := httptest.NewRequest(http.MethodGet, "/proxy/test/testpkg", nil)
		w1 := httptest.NewRecorder()
		handleProxy(w1, req1)

		if w1.Code != 200 {
			t.Fatalf("first request: expected 200, got %d: %s", w1.Code, w1.Body.String())
		}
		if w1.Header().Get("X-Proxy-Cache") != "MISS" {
			t.Errorf("first request should be MISS, got %s", w1.Header().Get("X-Proxy-Cache"))
		}
		if !strings.Contains(w1.Body.String(), "testpkg") {
			t.Errorf("expected response body, got: %s", w1.Body.String())
		}

		req2 := httptest.NewRequest(http.MethodGet, "/proxy/test/testpkg", nil)
		w2 := httptest.NewRecorder()
		handleProxy(w2, req2)

		if w2.Code != 200 {
			t.Fatalf("second request: expected 200, got %d", w2.Code)
		}
		if w2.Header().Get("X-Proxy-Cache") != "HIT" {
			t.Errorf("second request should be HIT, got %s", w2.Header().Get("X-Proxy-Cache"))
		}
	})
}

func TestHandleProxy_ImmutableCaching(t *testing.T) {
	var hitCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("tarball-content"))
	}))
	defer upstream.Close()

	withTestProxy(t, map[string]Registry{
		"test": {Name: "test", Upstream: upstream.URL, RewriteBody: false},
	}, func() {
		req := httptest.NewRequest(http.MethodGet, "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
		w := httptest.NewRecorder()
		handleProxy(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if hitCount.Load() != 1 {
			t.Fatalf("expected 1 upstream hit, got %d", hitCount.Load())
		}

		key := proxyCacheKey("test", "pkg/-/pkg-1.0.0.tgz")
		metaData, err := os.ReadFile(filepath.Join(proxyDir, "test", key+".meta"))
		if err != nil {
			t.Fatalf("reading meta: %v", err)
		}
		var meta proxyMeta
		json.Unmarshal(metaData, &meta)
		if !meta.Immutable {
			t.Error("tgz file should be marked immutable")
		}

		req2 := httptest.NewRequest(http.MethodGet, "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
		w2 := httptest.NewRecorder()
		handleProxy(w2, req2)
		_ = w2

		if hitCount.Load() != 1 {
			t.Errorf("expected no additional upstream hit, got %d total", hitCount.Load())
		}
	})
}

func TestHandleProxy_TTLExpiry(t *testing.T) {
	var hitCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":"1.0"}`))
	}))
	defer upstream.Close()

	oldTTL := proxyCacheTTL
	proxyCacheTTL = 10 * time.Second
	defer func() { proxyCacheTTL = oldTTL }()

	withTestProxy(t, map[string]Registry{
		"test": {Name: "test", Upstream: upstream.URL, RewriteBody: false},
	}, func() {
		req := httptest.NewRequest(http.MethodGet, "/proxy/test/metadata", nil)
		w := httptest.NewRecorder()
		handleProxy(w, req)
		if hitCount.Load() != 1 {
			t.Fatalf("expected 1 upstream hit, got %d", hitCount.Load())
		}

		req2 := httptest.NewRequest(http.MethodGet, "/proxy/test/metadata", nil)
		w2 := httptest.NewRecorder()
		handleProxy(w2, req2)
		_ = w2
		if hitCount.Load() != 1 {
			t.Errorf("expected cache hit, but got %d upstream hits", hitCount.Load())
		}

		expireProxyCacheEntry(t, "test", "metadata")

		req3 := httptest.NewRequest(http.MethodGet, "/proxy/test/metadata", nil)
		w3 := httptest.NewRecorder()
		handleProxy(w3, req3)
		_ = w3
		if hitCount.Load() != 2 {
			t.Errorf("expected 2 upstream hits after TTL expiry, got %d", hitCount.Load())
		}
	})
}

func TestHandleProxy_ConcurrentReads(t *testing.T) {
	var hitCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("cached-content"))
	}))
	defer upstream.Close()

	withTestProxy(t, map[string]Registry{
		"test": {Name: "test", Upstream: upstream.URL, RewriteBody: false},
	}, func() {
		req := httptest.NewRequest(http.MethodGet, "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
		w := httptest.NewRecorder()
		handleProxy(w, req)
		if hitCount.Load() != 1 {
			t.Fatalf("expected 1 upstream hit, got %d", hitCount.Load())
		}

		var wg sync.WaitGroup
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := httptest.NewRequest(http.MethodGet, "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
				rec := httptest.NewRecorder()
				handleProxy(rec, r)
				if rec.Header().Get("X-Proxy-Cache") != "HIT" {
					t.Errorf("concurrent read should be HIT, got %s", rec.Header().Get("X-Proxy-Cache"))
				}
			}()
		}
		wg.Wait()

		if hitCount.Load() != 1 {
			t.Errorf("expected exactly 1 upstream hit after concurrent reads, got %d", hitCount.Load())
		}
	})
}

func TestHandleProxy_UpstreamError(t *testing.T) {
	var callCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		if callCount.Load() == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":"cached"}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()

	oldTTL := proxyCacheTTL
	proxyCacheTTL = 1 * time.Millisecond
	defer func() { proxyCacheTTL = oldTTL }()

	withTestProxy(t, map[string]Registry{
		"test": {Name: "test", Upstream: upstream.URL, RewriteBody: false},
	}, func() {
		req1 := httptest.NewRequest(http.MethodGet, "/proxy/test/data", nil)
		w1 := httptest.NewRecorder()
		handleProxy(w1, req1)
		if w1.Code != 200 {
			t.Fatalf("expected 200, got %d", w1.Code)
		}

		expireProxyCacheEntry(t, "test", "data")

		req2 := httptest.NewRequest(http.MethodGet, "/proxy/test/data", nil)
		w2 := httptest.NewRecorder()
		handleProxy(w2, req2)
		if w2.Code != 502 {
			t.Errorf("expected 502 forwarded from upstream, got %d", w2.Code)
		}
	})
}

func TestProxyRewriteBody_Npm(t *testing.T) {
	body := []byte(`{"name":"lodash","dist":{"tarball":"https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz"}}`)
	reg := Registry{Name: "npm", Upstream: "https://registry.npmjs.org", RewriteBody: true}

	result := proxyRewriteBody(body, reg, "http://gitcache.local:8091/proxy")
	s := string(result)

	if strings.Contains(s, "registry.npmjs.org") {
		t.Errorf("upstream URL should have been rewritten, got: %s", s)
	}
	if !strings.Contains(s, "http://gitcache.local:8091/proxy/npm/lodash/-/lodash-4.17.21.tgz") {
		t.Errorf("expected proxy URL, got: %s", s)
	}
}

func TestProxyRewriteBody_Pypi(t *testing.T) {
	body := []byte(`<a href="https://files.pythonhosted.org/packages/ab/cd/requests-2.31.0.tar.gz">requests-2.31.0.tar.gz</a>`)
	reg := Registry{Name: "pypi", Upstream: "https://pypi.org", RewriteBody: true}

	result := proxyRewriteBody(body, reg, "http://gitcache.local:8091/proxy")
	s := string(result)

	if strings.Contains(s, "files.pythonhosted.org") {
		t.Errorf("pythonhosted URL should have been rewritten, got: %s", s)
	}
	if !strings.Contains(s, "http://gitcache.local:8091/proxy/pythonhosted/packages/ab/cd/requests-2.31.0.tar.gz") {
		t.Errorf("expected proxy URL, got: %s", s)
	}
}

func TestProxyRewriteBody_NoRewrite(t *testing.T) {
	body := []byte(`{"some":"data"}`)
	reg := Registry{Name: "rubygems", Upstream: "https://rubygems.org", RewriteBody: false}

	result := proxyRewriteBody(body, reg, "http://gitcache.local:8091/proxy")
	if string(result) != string(body) {
		t.Errorf("non-rewrite registry should return body unchanged")
	}
}

func TestHandleProxyStats(t *testing.T) {
	oldProxyDir := proxyDir
	proxyDir = t.TempDir()
	defer func() { proxyDir = oldProxyDir }()

	os.MkdirAll(filepath.Join(proxyDir, "npm"), 0o755)
	os.WriteFile(filepath.Join(proxyDir, "npm", "abc123.body"), []byte("cached content"), 0o644)
	os.WriteFile(filepath.Join(proxyDir, "npm", "abc123.meta"), []byte("{}"), 0o644)

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()
	handleProxyStats(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var stats map[string]any
	json.NewDecoder(w.Body).Decode(&stats)

	total, ok := stats["total"].(map[string]any)
	if !ok {
		t.Fatal("expected 'total' in stats")
	}
	files := total["files"].(float64)
	if files != 1 {
		t.Errorf("expected 1 cached file, got %.0f", files)
	}
}

func npmProxyRequest(host, path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/proxy/npm/"+path, nil)
	req.Host = host
	return req
}

func TestHandleProxy_ForgedHostDoesNotPoisonLaterRequests(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"pkg","dist":{"tarball":"%s/pkg/-/pkg-1.0.0.tgz"}}`, upstream.URL)
	}))
	defer upstream.Close()

	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: upstream.URL, RewriteBody: true},
	}, func() {
		w1 := httptest.NewRecorder()
		handleProxy(w1, npmProxyRequest("evil.example.com", "pkg"))
		if w1.Header().Get("X-Proxy-Cache") != "MISS" {
			t.Fatalf("first request should be MISS, got %s", w1.Header().Get("X-Proxy-Cache"))
		}
		if !strings.Contains(w1.Body.String(), "http://evil.example.com/proxy/npm/") {
			t.Fatalf("attacker response should carry its own Host, got: %s", w1.Body.String())
		}

		w2 := httptest.NewRecorder()
		handleProxy(w2, npmProxyRequest("cache.internal", "pkg"))
		if w2.Header().Get("X-Proxy-Cache") != "HIT" {
			t.Fatalf("second request should be HIT, got %s", w2.Header().Get("X-Proxy-Cache"))
		}
		body := w2.Body.String()
		if strings.Contains(body, "evil.example.com") {
			t.Errorf("cached body was poisoned by the forged Host: %s", body)
		}
		if !strings.Contains(body, "http://cache.internal/proxy/npm/pkg/-/pkg-1.0.0.tgz") {
			t.Errorf("expected a rewrite against the second request Host, got: %s", body)
		}

		cached, err := os.ReadFile(filepath.Join(proxyDir, "npm", proxyCacheKey("npm", "pkg")+".body"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(cached), upstream.URL) {
			t.Errorf("cached body should stay unrewritten, got: %s", cached)
		}
	})
}

func TestHandleProxy_PublicBaseRewritesAndCaches(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"pkg","dist":{"tarball":"%s/pkg/-/pkg-1.0.0.tgz"}}`, upstream.URL)
	}))
	defer upstream.Close()

	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: upstream.URL, RewriteBody: true},
	}, func() {
		proxyPublicBase = "http://cache.internal:8090/proxy"

		w1 := httptest.NewRecorder()
		handleProxy(w1, npmProxyRequest("evil.example.com", "pkg"))
		if !strings.Contains(w1.Body.String(), "http://cache.internal:8090/proxy/npm/pkg/-/pkg-1.0.0.tgz") {
			t.Errorf("expected the configured base, got: %s", w1.Body.String())
		}

		cached, err := os.ReadFile(filepath.Join(proxyDir, "npm", proxyCacheKey("npm", "pkg")+".body"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(cached), "http://cache.internal:8090/proxy/npm/") {
			t.Errorf("configured base should be cached rewritten, got: %s", cached)
		}

		w2 := httptest.NewRecorder()
		handleProxy(w2, npmProxyRequest("cache.internal:8090", "pkg"))
		if strings.Contains(w2.Body.String(), "evil.example.com") {
			t.Errorf("configured base must ignore the request Host, got: %s", w2.Body.String())
		}
	})
}

func TestProxyBaseForRequest(t *testing.T) {
	tests := []struct {
		name          string
		publicBase    string
		trustForward  bool
		host          string
		forwardedHost string
		forwardedProt string
		want          string
		wantOK        bool
	}{
		{name: "request host", host: "cache.internal:8090", want: "http://cache.internal:8090/proxy", wantOK: true},
		{
			name:          "forwarded host ignored by default",
			host:          "cache.internal",
			forwardedHost: "evil.example.com",
			want:          "http://cache.internal/proxy",
			wantOK:        true,
		},
		{
			name:          "right-most forwarded host wins when trusted",
			trustForward:  true,
			host:          "cache.internal",
			forwardedHost: "evil.example.com, cache.example.com",
			forwardedProt: "http, https",
			want:          "https://cache.example.com/proxy",
			wantOK:        true,
		},
		{
			name:          "malformed forwarded host is rejected",
			trustForward:  true,
			host:          "cache.internal",
			forwardedHost: `x"}}`,
			wantOK:        false,
		},
		{
			name:          "forwarded host with a path is rejected",
			trustForward:  true,
			host:          "cache.internal",
			forwardedHost: "evil.example.com/proxy/npm",
			wantOK:        false,
		},
		{
			name:          "container host names are accepted",
			trustForward:  true,
			host:          "cache.internal",
			forwardedHost: "sparkwing_cache:8090",
			want:          "http://sparkwing_cache:8090/proxy",
			wantOK:        true,
		},
		{
			name:          "bracketed ipv6 forwarded host is accepted",
			trustForward:  true,
			host:          "cache.internal",
			forwardedHost: "[2001:db8::1]:8090",
			want:          "http://[2001:db8::1]:8090/proxy",
			wantOK:        true,
		},
		{
			name:          "empty host without a trusted forwarded host is rejected",
			forwardedHost: "cache.example.com",
			wantOK:        false,
		},
		{
			name:          "empty host falls back to a trusted forwarded host",
			trustForward:  true,
			forwardedHost: "cache.example.com",
			want:          "http://cache.example.com/proxy",
			wantOK:        true,
		},
		{
			name:          "configured base wins",
			publicBase:    "http://configured.internal/proxy",
			trustForward:  true,
			host:          "cache.internal",
			forwardedHost: "evil.example.com",
			want:          "http://configured.internal/proxy",
			wantOK:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldBase, oldTrust := proxyPublicBase, proxyTrustForwardedHost
			proxyPublicBase, proxyTrustForwardedHost = tt.publicBase, tt.trustForward
			defer func() { proxyPublicBase, proxyTrustForwardedHost = oldBase, oldTrust }()

			req := httptest.NewRequest(http.MethodGet, "/proxy/npm/pkg", nil)
			req.Host = tt.host
			if tt.forwardedHost != "" {
				req.Header.Set("X-Forwarded-Host", tt.forwardedHost)
			}
			if tt.forwardedProt != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProt)
			}
			got, ok := proxyBaseForRequest(req)
			if ok != tt.wantOK {
				t.Fatalf("proxyBaseForRequest() ok = %v, want %v (got %q)", ok, tt.wantOK, got)
			}
			if ok && got != tt.want {
				t.Errorf("proxyBaseForRequest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeProxyPublicBase(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "  ", want: ""},
		{in: "http://cache.internal:8090", want: "http://cache.internal:8090/proxy"},
		{in: "https://cache.example.com/", want: "https://cache.example.com/proxy"},
		{in: "https://cache.example.com/proxy", want: "https://cache.example.com/proxy"},
		{in: "cache.internal:8090", wantErr: true},
		{in: "ftp://cache.internal", wantErr: true},
		{in: "://", wantErr: true},
		{in: "http://cache.internal//", wantErr: true},
		{in: "http://cache.internal/proxy/npm", wantErr: true},
		{in: "http://cache.internal/a/b/", wantErr: true},
		{in: "http://cache.internal/?a=b", wantErr: true},
		{in: "http://cache.internal/#frag", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := normalizeProxyPublicBase(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("normalizeProxyPublicBase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func writeCachedProxyEntry(t *testing.T, registry, path string, body []byte, immutable bool) {
	t.Helper()
	key := proxyCacheKey(registry, path)
	meta := proxyMeta{
		Path:        path,
		ContentType: "application/json",
		CachedAt:    time.Now().Unix(),
		Size:        int64(len(body)),
		Immutable:   immutable,
		StatusCode:  200,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyDir, registry, key+".body"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyDir, registry, key+".meta"), metaJSON, 0o644); err != nil {
		t.Fatal(err)
	}
}

func largePackument(t *testing.T, upstream string, size int) []byte {
	t.Helper()
	filler := strings.Repeat("x", 4096)
	var b strings.Builder
	for b.Len() < size {
		fmt.Fprintf(&b, `{"tarball":"%s/pkg/-/pkg-1.0.0.tgz","pad":"%s"}`+"\n", upstream, filler)
	}
	return []byte(b.String())
}

func TestHandleProxy_HeadOnLargeMutableEntryDoesNotReadTheBody(t *testing.T) {
	const upstream = "https://registry.npmjs.org"
	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: upstream, RewriteBody: true},
	}, func() {
		body := largePackument(t, upstream, 8<<20)
		writeCachedProxyEntry(t, "npm", "pkg", body, false)

		req := httptest.NewRequest(http.MethodHead, "/proxy/npm/pkg", nil)
		req.Host = "cache.internal"
		w := httptest.NewRecorder()

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		handleProxy(w, req)
		runtime.ReadMemStats(&after)

		if w.Header().Get("X-Proxy-Cache") != "HIT" {
			t.Fatalf("expected a cache HIT, got %q", w.Header().Get("X-Proxy-Cache"))
		}
		if w.Body.Len() != 0 {
			t.Errorf("HEAD wrote %d body bytes", w.Body.Len())
		}
		allocated := after.TotalAlloc - before.TotalAlloc
		t.Logf("HEAD on a %d byte cached entry allocated %d bytes", len(body), allocated)
		if limit := uint64(len(body) / 8); allocated > limit {
			t.Errorf("HEAD on a %d byte entry allocated %d bytes, want under %d", len(body), allocated, limit)
		}

		get := httptest.NewRequest(http.MethodGet, "/proxy/npm/pkg", nil)
		get.Host = "cache.internal"
		gw := httptest.NewRecorder()
		handleProxy(gw, get)
		if strings.Contains(gw.Body.String(), upstream) {
			t.Error("streamed GET should have rewritten every upstream URL")
		}
		if want := strings.Count(string(body), upstream); strings.Count(gw.Body.String(), "http://cache.internal/proxy/npm/") != want {
			t.Errorf("streamed GET rewrote %d occurrences, want %d",
				strings.Count(gw.Body.String(), "http://cache.internal/proxy/npm/"), want)
		}
	})
}

func TestProxyStreamReplace(t *testing.T) {
	const old = "https://registry.npmjs.org"
	const replacement = "http://cache.internal/proxy/npm"

	offsets := []int{0, 17, proxyRewriteChunk - len(old) - 1, proxyRewriteChunk - len(old)/2, proxyRewriteChunk - 1, 2*proxyRewriteChunk + 5}
	for _, offset := range offsets {
		t.Run(fmt.Sprintf("offset-%d", offset), func(t *testing.T) {
			body := strings.Repeat("a", offset) + old + strings.Repeat("b", proxyRewriteChunk)
			var out strings.Builder
			if err := proxyStreamReplace(&out, strings.NewReader(body), old, replacement); err != nil {
				t.Fatal(err)
			}
			if want := strings.ReplaceAll(body, old, replacement); out.String() != want {
				t.Errorf("stream rewrite differs from a whole-body rewrite at offset %d", offset)
			}
		})
	}

	t.Run("one byte reads", func(t *testing.T) {
		body := "head " + old + " tail " + old
		var out strings.Builder
		if err := proxyStreamReplace(&out, iotest.OneByteReader(strings.NewReader(body)), old, replacement); err != nil {
			t.Fatal(err)
		}
		if want := strings.ReplaceAll(body, old, replacement); out.String() != want {
			t.Errorf("proxyStreamReplace() = %q, want %q", out.String(), want)
		}
	})

	t.Run("no rule copies through", func(t *testing.T) {
		var out strings.Builder
		if err := proxyStreamReplace(&out, strings.NewReader("body"), "", ""); err != nil {
			t.Fatal(err)
		}
		if out.String() != "body" {
			t.Errorf("proxyStreamReplace() = %q, want %q", out.String(), "body")
		}
	})
}

func TestHandleProxy_ServeTimeRewritesAreNotSharedCacheable(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"pkg","dist":{"tarball":"%s/pkg/-/pkg-1.0.0.tgz"}}`, upstream.URL)
	}))
	defer upstream.Close()

	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: upstream.URL, RewriteBody: true},
	}, func() {
		miss := httptest.NewRecorder()
		handleProxy(miss, npmProxyRequest("cache.internal", "pkg"))
		if got := miss.Header().Get("Cache-Control"); got != "private, max-age=0" {
			t.Errorf("MISS Cache-Control = %q, want %q", got, "private, max-age=0")
		}
		if got := miss.Header().Get("Vary"); got != "Host" {
			t.Errorf("MISS Vary = %q, want %q", got, "Host")
		}

		hit := httptest.NewRecorder()
		handleProxy(hit, npmProxyRequest("cache.internal", "pkg"))
		if got := hit.Header().Get("Cache-Control"); got != "private, max-age=0" {
			t.Errorf("HIT Cache-Control = %q, want %q", got, "private, max-age=0")
		}
		if got := hit.Header().Get("Vary"); got != "Host" {
			t.Errorf("HIT Vary = %q, want %q", got, "Host")
		}

		proxyTrustForwardedHost = true
		trusted := httptest.NewRecorder()
		handleProxy(trusted, npmProxyRequest("cache.internal", "pkg"))
		if got := trusted.Header().Get("Vary"); got != "Host, X-Forwarded-Host" {
			t.Errorf("trusted Vary = %q, want %q", got, "Host, X-Forwarded-Host")
		}
	})
}

func TestHandleProxy_ConfiguredBaseStaysPubliclyCacheable(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"pkg","dist":{"tarball":"%s/pkg/-/pkg-1.0.0.tgz"}}`, upstream.URL)
	}))
	defer upstream.Close()

	oldTTL := proxyCacheTTL
	proxyCacheTTL = 10 * time.Minute
	defer func() { proxyCacheTTL = oldTTL }()

	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: upstream.URL, RewriteBody: true},
	}, func() {
		proxyPublicBase = "http://cache.internal:8090/proxy"

		miss := httptest.NewRecorder()
		handleProxy(miss, npmProxyRequest("cache.internal:8090", "pkg"))
		if got := miss.Header().Get("Cache-Control"); got != "public, max-age=600" {
			t.Errorf("MISS Cache-Control = %q, want %q", got, "public, max-age=600")
		}
		if got := miss.Header().Get("Vary"); got != "" {
			t.Errorf("MISS Vary = %q, want none", got)
		}

		hit := httptest.NewRecorder()
		handleProxy(hit, npmProxyRequest("cache.internal:8090", "pkg"))
		if got := hit.Header().Get("Cache-Control"); !strings.HasPrefix(got, "public, max-age=") {
			t.Errorf("HIT Cache-Control = %q, want a public max-age", got)
		}
	})
}

func TestHandleProxy_MissingHostIsRejected(t *testing.T) {
	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: "https://registry.npmjs.org", RewriteBody: true},
	}, func() {
		req := httptest.NewRequest(http.MethodGet, "/proxy/npm/pkg", nil)
		req.Host = ""
		req.Header.Set("X-Forwarded-Host", "evil.example.com")
		w := httptest.NewRecorder()
		handleProxy(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for a request with no usable Host, got %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "evil.example.com") {
			t.Errorf("untrusted forwarded host leaked into the response: %s", w.Body.String())
		}
	})
}
