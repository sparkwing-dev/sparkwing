package cache

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type proxyFixtureTransport func(*http.Request) (*http.Response, error)

func (f proxyFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type proxyDiscardResponse struct {
	header http.Header
	status int
	bytes  int64
}

func (w *proxyDiscardResponse) Header() http.Header    { return w.header }
func (w *proxyDiscardResponse) WriteHeader(status int) { w.status = status }
func (w *proxyDiscardResponse) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	return len(p), nil
}

func TestProxyMissMemoryDoesNotScaleWithBody(t *testing.T) {
	const upstream = "https://registry.example"
	body := bytes.Repeat([]byte(upstream+"/pkg.tgz\n"), (8<<20)/(len(upstream)+9))
	for _, mode := range []string{"immutable", "request-rewrite", "public-rewrite"} {
		t.Run(mode, func(t *testing.T) {
			withTestProxy(t, map[string]Registry{"npm": {Name: "npm", Upstream: upstream, RewriteBody: true}}, func() {
				client := proxyClient
				proxyClient = &http.Client{Transport: proxyFixtureTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
				})}
				defer func() { proxyClient = client }()
				path := "pkg"
				if mode == "immutable" {
					path += ".tgz"
				}
				if mode == "public-rewrite" {
					proxyPublicBase = "http://cache.internal/proxy"
				}
				w := &proxyDiscardResponse{header: make(http.Header)}
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				handleProxy(w, npmProxyRequest("cache.internal", path))
				runtime.ReadMemStats(&after)
				if w.header.Get("X-Proxy-Cache") != "MISS" || w.bytes < int64(len(body)) {
					t.Fatalf("incomplete cache miss: headers=%v bytes=%d", w.header, w.bytes)
				}
				allocated := after.TotalAlloc - before.TotalAlloc
				t.Logf("%s miss allocated %d bytes for %d upstream bytes", mode, allocated, len(body))
				if allocated > 2<<20 {
					t.Fatalf("%s miss allocated %d bytes for %d upstream bytes", mode, allocated, len(body))
				}
			})
		})
	}
}

func TestProxyMissSurvivesCachePublicationFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := io.WriteString(w, "package bytes")
		if err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	withTestProxy(t, map[string]Registry{"test": {Name: "test", Upstream: upstream.URL}}, func() {
		// safety: the upstream stays healthy so only cache publication can explain a failed download.
		if err := os.Mkdir(filepath.Join(proxyDir, "test", proxyCacheKey("test", "pkg.tgz", "")+".body"), 0o755); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			w := httptest.NewRecorder()
			handleProxy(w, httptest.NewRequest(http.MethodGet, "/proxy/test/pkg.tgz", nil))
			if w.Code != http.StatusOK || w.Body.String() != "package bytes" || w.Header().Get("X-Proxy-Cache") != "MISS" {
				t.Fatalf("cache write failure changed download: status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
			}
		}
		entries, err := os.ReadDir(filepath.Join(proxyDir, "test"))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") || strings.HasSuffix(entry.Name(), ".meta") {
				t.Errorf("failed publication left %s", entry.Name())
			}
		}
	})
}

func TestProxyIndexPageKeepsItsDownloadURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "registry index bytes"); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	withTestProxy(t, map[string]Registry{"test": {Name: "test", Upstream: upstream.URL}}, func() {
		for _, status := range []string{"MISS", "HIT"} {
			w := httptest.NewRecorder()
			handleProxy(w, httptest.NewRequest(http.MethodGet, "/proxy/test/index.html", nil))
			if w.Code != http.StatusOK || w.Body.String() != "registry index bytes" || w.Header().Get("X-Proxy-Cache") != status {
				t.Fatalf("index response: code=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
			}
		}
	})
}
