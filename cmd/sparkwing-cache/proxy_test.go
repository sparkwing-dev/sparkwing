package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

	// Different registries, same path
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
	req := httptest.NewRequest("GET", "/proxy/foobar/something", nil)
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
	req := httptest.NewRequest("GET", "/proxy/npm/../../etc/passwd", nil)
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for path traversal, got %d", w.Code)
	}
}

func TestHandleProxy_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest("POST", "/proxy/npm/lodash", nil)
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405 for POST, got %d", w.Code)
	}
}

func TestHandleProxy_MissingRegistry(t *testing.T) {
	req := httptest.NewRequest("GET", "/proxy/", nil)
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
	defaultRegistries = registries
	proxyDir = t.TempDir()
	for name := range registries {
		os.MkdirAll(filepath.Join(proxyDir, name), 0o755)
	}
	defer func() {
		defaultRegistries = oldRegistries
		proxyDir = oldProxyDir
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
		// First request: cache miss
		req1 := httptest.NewRequest("GET", "/proxy/test/testpkg", nil)
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

		// Second request: cache hit
		req2 := httptest.NewRequest("GET", "/proxy/test/testpkg", nil)
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
		req := httptest.NewRequest("GET", "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
		w := httptest.NewRecorder()
		handleProxy(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if hitCount.Load() != 1 {
			t.Fatalf("expected 1 upstream hit, got %d", hitCount.Load())
		}

		// Verify meta says immutable
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

		// Second fetch should come from cache
		req2 := httptest.NewRequest("GET", "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
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
	proxyCacheTTL = 1 * time.Second
	defer func() { proxyCacheTTL = oldTTL }()

	withTestProxy(t, map[string]Registry{
		"test": {Name: "test", Upstream: upstream.URL, RewriteBody: false},
	}, func() {
		req := httptest.NewRequest("GET", "/proxy/test/metadata", nil)
		w := httptest.NewRecorder()
		handleProxy(w, req)
		if hitCount.Load() != 1 {
			t.Fatalf("expected 1 upstream hit, got %d", hitCount.Load())
		}

		// Immediately: should be cached
		req2 := httptest.NewRequest("GET", "/proxy/test/metadata", nil)
		w2 := httptest.NewRecorder()
		handleProxy(w2, req2)
		_ = w2
		if hitCount.Load() != 1 {
			t.Errorf("expected cache hit, but got %d upstream hits", hitCount.Load())
		}

		// Wait for TTL to expire
		time.Sleep(1100 * time.Millisecond)

		// Should re-fetch
		req3 := httptest.NewRequest("GET", "/proxy/test/metadata", nil)
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
		// Prime the cache
		req := httptest.NewRequest("GET", "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
		w := httptest.NewRecorder()
		handleProxy(w, req)
		if hitCount.Load() != 1 {
			t.Fatalf("expected 1 upstream hit, got %d", hitCount.Load())
		}

		// Fire 10 concurrent reads — all should get cache hits, no upstream calls
		var wg sync.WaitGroup
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := httptest.NewRequest("GET", "/proxy/test/pkg/-/pkg-1.0.0.tgz", nil)
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
		w.WriteHeader(502)
	}))
	defer upstream.Close()

	oldTTL := proxyCacheTTL
	proxyCacheTTL = 1 * time.Millisecond
	defer func() { proxyCacheTTL = oldTTL }()

	withTestProxy(t, map[string]Registry{
		"test": {Name: "test", Upstream: upstream.URL, RewriteBody: false},
	}, func() {
		// First request — populates cache
		req1 := httptest.NewRequest("GET", "/proxy/test/data", nil)
		w1 := httptest.NewRecorder()
		handleProxy(w1, req1)
		if w1.Code != 200 {
			t.Fatalf("expected 200, got %d", w1.Code)
		}

		time.Sleep(10 * time.Millisecond)

		// Upstream returns 502 — forwarded to client
		req2 := httptest.NewRequest("GET", "/proxy/test/data", nil)
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

	req := httptest.NewRequest("GET", "/proxy/npm/lodash", nil)
	req.Host = "gitcache.local:8091"

	result := proxyRewriteBody(body, reg, req)
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

	req := httptest.NewRequest("GET", "/proxy/pypi/simple/requests/", nil)
	req.Host = "gitcache.local:8091"

	result := proxyRewriteBody(body, reg, req)
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

	req := httptest.NewRequest("GET", "/proxy/rubygems/api/v1/gems/rails.json", nil)
	req.Host = "gitcache.local:8091"

	result := proxyRewriteBody(body, reg, req)
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

	req := httptest.NewRequest("GET", "/stats", nil)
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
