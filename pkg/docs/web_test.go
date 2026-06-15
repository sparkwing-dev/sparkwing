package docs

import (
	"context"
	"errors"
	"fmt"
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

// newTestClient returns a WebClient pointed at a httptest server with
// a fresh per-test cache dir. The handler observes every request via
// hits so tests can assert retry / cache-hit behavior.
func newTestClient(t *testing.T, handler http.Handler) (*WebClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cacheDir := t.TempDir()
	return &WebClient{
		BaseURL:   srv.URL,
		HTTP:      &http.Client{Timeout: 5 * time.Second},
		CacheDir:  cacheDir,
		UserAgent: "sparkwing-test",
	}, srv
}

func TestWebClient_Versions_HappyPath(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/versions.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0","v0.3.0"]}`))
	}))
	got, err := c.Versions(context.Background())
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if got.Latest != "v0.4.0" {
		t.Errorf("Latest = %q, want v0.4.0", got.Latest)
	}
	if len(got.Versions) != 2 || got.Versions[0] != "v0.4.0" {
		t.Errorf("Versions = %v, want [v0.4.0, v0.3.0]", got.Versions)
	}
}

func TestWebClient_Versions_404IsErrNotFound(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	_, err := c.Versions(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestWebClient_5xxRetriesOnceThenErrUnavailable(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := c.Versions(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("expected 2 attempts (1 retry); got %d", got)
	}
}

func TestWebClient_4xxNon404DoesNotRetry(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	_, err := c.Versions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx); got %d", got)
	}
}

func TestWebClient_5xxThenSuccessRecovers(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"latest":"v1.0.0","versions":["v1.0.0"]}`))
	}))
	got, err := c.Versions(context.Background())
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if got.Latest != "v1.0.0" {
		t.Errorf("Latest = %q", got.Latest)
	}
}

func TestWebClient_CacheHitSkipsHTTP(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0"]}`))
	}))
	for range 3 {
		if _, err := c.Versions(context.Background()); err != nil {
			t.Fatalf("Versions: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected 1 HTTP hit (rest from cache); got %d", got)
	}
}

func TestWebClient_NoCacheBypassesCache(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0"]}`))
	}))
	c.NoCache = true
	for range 3 {
		if _, err := c.Versions(context.Background()); err != nil {
			t.Fatalf("Versions: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("expected 3 HTTP hits with NoCache; got %d", got)
	}
}

func TestWebClient_ExpiredCacheRefetches(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0"]}`))
	}))
	if _, err := c.Versions(context.Background()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	metaPath := filepath.Join(c.CacheDir, "versions.json.meta")
	expired := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"fetched_at":"%s","status":200,"url":"x"}`, expired)
	if err := os.WriteFile(metaPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Versions(context.Background()); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("expected 2 HTTP hits (one cache miss after expiry); got %d", got)
	}
}

func TestWebClient_DocVersioned(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs/v0.3.0/pipelines.md" {
			_, _ = w.Write([]byte("# Pipelines (v0.3.0)\n"))
			return
		}
		http.NotFound(w, r)
	}))
	body, err := c.Doc(context.Background(), "v0.3.0", "pipelines")
	if err != nil {
		t.Fatalf("Doc: %v", err)
	}
	if !strings.Contains(body, "v0.3.0") {
		t.Errorf("body missing v0.3.0 marker: %q", body)
	}
}

func TestWebClient_DocLatestUsesUnversionedPath(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs/pipelines.md" {
			_, _ = w.Write([]byte("# Pipelines (latest)\n"))
			return
		}
		http.NotFound(w, r)
	}))
	for _, v := range []string{"", LatestAlias} {
		body, err := c.Doc(context.Background(), v, "pipelines")
		if err != nil {
			t.Fatalf("Doc(%q): %v", v, err)
		}
		if !strings.Contains(body, "latest") {
			t.Errorf("Doc(%q) body = %q", v, body)
		}
	}
}

func TestWebClient_DocRejectsMalformedSlug(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("HTTP should not be called for malformed slug; got %s", r.URL.Path)
	}))
	for _, bad := range []string{"", "/absolute", "../escape", "foo/../bar", "foo/."} {
		if _, err := c.Doc(context.Background(), "v0.3.0", bad); err == nil {
			t.Errorf("Doc(%q) should error", bad)
		}
	}
}

func TestWebClient_MigrationFetch(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/migrations/v0.3.0.md" {
			_, _ = w.Write([]byte("# Migrating to v0.3.0\n"))
			return
		}
		http.NotFound(w, r)
	}))
	body, err := c.Migration(context.Background(), "v0.3.0")
	if err != nil {
		t.Fatalf("Migration: %v", err)
	}
	if !strings.Contains(body, "v0.3.0") {
		t.Errorf("body = %q", body)
	}
}

func TestWebClient_MigrationRejectsLatest(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("HTTP should not be called")
	}))
	if _, err := c.Migration(context.Background(), LatestAlias); err == nil {
		t.Error("Migration(latest) should error")
	}
	if _, err := c.Migration(context.Background(), ""); err == nil {
		t.Error("Migration(\"\") should error")
	}
}

func TestWebClient_MigrationIndex(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/migrations/index.json" {
			_, _ = w.Write([]byte(`[{"version":"v0.4.0","slug":"v0.4.0","title":"x","date":"2026-05-20","summary":"s","bytes":10}]`))
			return
		}
		http.NotFound(w, r)
	}))
	got, err := c.MigrationIndex(context.Background())
	if err != nil {
		t.Fatalf("MigrationIndex: %v", err)
	}
	if len(got) != 1 || got[0].Version != "v0.4.0" {
		t.Errorf("unexpected result %+v", got)
	}
}

func TestWebClient_DocIndexVersioned(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs/v0.3.0/index.json" {
			_, _ = w.Write([]byte(`[{"slug":"pipelines","title":"P","summary":"s","bytes":1}]`))
			return
		}
		http.NotFound(w, r)
	}))
	got, err := c.DocIndex(context.Background(), "v0.3.0")
	if err != nil {
		t.Fatalf("DocIndex: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "pipelines" {
		t.Errorf("unexpected result %+v", got)
	}
}

func TestWebClient_MalformedJSONErrors(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	if _, err := c.Versions(context.Background()); err == nil {
		t.Error("expected parse error")
	}
}

func TestWebClient_ConcurrentWritesDoNotCorrupt(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0"]}`))
	}))
	c.NoCache = true
	c.NoCache = false
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Versions(context.Background())
		}()
	}
	wg.Wait()
	body, err := os.ReadFile(filepath.Join(c.CacheDir, "versions.json"))
	if err != nil {
		t.Fatalf("read cached body: %v", err)
	}
	if !strings.Contains(string(body), "v0.4.0") {
		t.Errorf("cached body corrupted: %q", body)
	}
}

func TestClearCache_RemovesEverythingInsideButNothingOutside(t *testing.T) {
	c := &WebClient{CacheDir: t.TempDir()}
	for _, rel := range []string{
		"versions.json",
		"docs/pipelines.md",
		"docs/v0.3.0/pipelines.md",
		"migrations/v0.3.0.md",
	} {
		p := filepath.Join(c.CacheDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(filepath.Dir(c.CacheDir), "outside.txt")
	if err := os.WriteFile(outside, []byte("don't touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	n, err := c.ClearCache()
	if err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if n != 4 {
		t.Errorf("removed = %d, want 4", n)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("sentinel outside cache should still exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.CacheDir, "versions.json")); !os.IsNotExist(err) {
		t.Errorf("expected cache file removed; stat err = %v", err)
	}
}

func TestClearCache_HandlesMissingDir(t *testing.T) {
	c := &WebClient{CacheDir: filepath.Join(t.TempDir(), "never-created")}
	n, err := c.ClearCache()
	if err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

func TestCacheInfo_CountsByCategory(t *testing.T) {
	c := &WebClient{CacheDir: t.TempDir()}
	writeFile := func(rel string) {
		p := filepath.Join(c.CacheDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("versions.json")
	meta := fmt.Sprintf(`{"fetched_at":"%s","status":200,"url":"x"}`, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(c.CacheDir, "versions.json.meta"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile("docs/pipelines.md")
	writeFile("docs/v0.3.0/pipelines.md")
	writeFile("docs/v0.3.0/index.json")
	writeFile("migrations/v0.3.0.md")

	stats, err := c.CacheInfo()
	if err != nil {
		t.Fatalf("CacheInfo: %v", err)
	}
	if !stats.Exists {
		t.Fatal("expected Exists=true")
	}
	if stats.DocFiles != 2 {
		t.Errorf("DocFiles = %d, want 2", stats.DocFiles)
	}
	if stats.MigrationFiles != 1 {
		t.Errorf("MigrationFiles = %d, want 1", stats.MigrationFiles)
	}
	if stats.IndexFiles != 1 {
		t.Errorf("IndexFiles = %d, want 1", stats.IndexFiles)
	}
	if !strings.HasPrefix(stats.VersionsState, "fresh") {
		t.Errorf("VersionsState = %q, want fresh prefix", stats.VersionsState)
	}
}

func TestNewWebClient_HonorsEnvOverride(t *testing.T) {
	t.Setenv(BaseURLEnvVar, "http://localhost:9999")
	c := NewWebClient()
	if c.BaseURL != "http://localhost:9999" {
		t.Errorf("BaseURL = %q, want env override", c.BaseURL)
	}
}

func TestNewWebClient_TrimsTrailingSlash(t *testing.T) {
	t.Setenv(BaseURLEnvVar, "http://example.com/")
	c := NewWebClient()
	if c.BaseURL != "http://example.com" {
		t.Errorf("BaseURL = %q, want trailing-slash stripped", c.BaseURL)
	}
}

func TestNewWebClient_FallsBackToDefault(t *testing.T) {
	t.Setenv(BaseURLEnvVar, "")
	c := NewWebClient()
	if c.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want default", c.BaseURL)
	}
}

func TestCachePath_RefusesToEscape(t *testing.T) {
	c := &WebClient{CacheDir: t.TempDir()}
	bad := []string{"/../../etc/passwd"}
	for _, p := range bad {
		got := c.cachePath(p)
		if got != "" && !strings.HasPrefix(got, c.CacheDir) {
			t.Errorf("cachePath(%q) = %q escapes %q", p, got, c.CacheDir)
		}
	}
}
