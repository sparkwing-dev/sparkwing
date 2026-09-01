package sparkwing

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
)

func TestDeriveDepCacheKeyStableAndSafe(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "go.sum")
	if err := os.WriteFile(lock, []byte("example.com/mod v1.0.0 h1:abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	k1, err := deriveDepCacheKey("go-modules", "", lock)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	k2, err := deriveDepCacheKey("go-modules", "", lock)
	if err != nil {
		t.Fatalf("derive again: %v", err)
	}
	if k1 != k2 {
		t.Fatalf("key not stable: %q vs %q", k1, k2)
	}
	if !depCacheKeyRE.MatchString(k1) {
		t.Fatalf("key %q fails the cache service's key pattern", k1)
	}
	wantPrefix := "dep-go-modules-" + goruntime.GOOS + "-" + goruntime.GOARCH + "-"
	if !strings.HasPrefix(k1, wantPrefix) {
		t.Fatalf("key %q missing prefix %q", k1, wantPrefix)
	}

	if err := os.WriteFile(lock, []byte("example.com/mod v1.1.0 h1:def\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k3, err := deriveDepCacheKey("go-modules", "", lock)
	if err != nil {
		t.Fatalf("derive after edit: %v", err)
	}
	if k3 == k1 {
		t.Fatal("key unchanged after lockfile edit")
	}
}

func TestDeriveDepCacheKeySanitizesName(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	if err := os.WriteFile(lock, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := deriveDepCacheKey("weird/name with spaces", "", lock)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if !depCacheKeyRE.MatchString(key) {
		t.Fatalf("sanitized key %q still fails the pattern", key)
	}
}

func populateDepCacheFixture(t *testing.T, dir string) {
	t.Helper()
	sub := filepath.Join(dir, "example.com", "mod@v1.0.0")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "mod.go"), []byte("package mod\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tool.sh", filepath.Join(dir, "tool-link")); err != nil {
		t.Fatal(err)
	}
}

func assertDepCacheFixture(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "example.com", "mod@v1.0.0", "mod.go"))
	if err != nil {
		t.Fatalf("restored file: %v", err)
	}
	if string(data) != "package mod\n" {
		t.Fatalf("restored content mismatch: %q", data)
	}
	fi, err := os.Stat(filepath.Join(dir, "tool.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("tool.sh lost its exec bit: %v", fi.Mode())
	}
	target, err := os.Readlink(filepath.Join(dir, "tool-link"))
	if err != nil {
		t.Fatalf("symlink not restored: %v", err)
	}
	if target != "tool.sh" {
		t.Fatalf("symlink target mismatch: %q", target)
	}
}

func TestDepCacheArchiveRoundTrip(t *testing.T) {
	src := t.TempDir()
	populateDepCacheFixture(t, src)

	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDepCacheArchive(f, src); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractDepCacheArchive(rf, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	assertDepCacheFixture(t, dest)
}

func TestExtractDepCacheArchiveRejectsEscape(t *testing.T) {
	if _, err := securePathJoin("/safe/root", "../evil"); err == nil {
		t.Fatal("path escaping via .. was not rejected")
	}
	if _, err := securePathJoin("/safe/root", "/abs/evil"); err == nil {
		t.Fatal("absolute path was not rejected")
	}
	if _, err := securePathJoin("/safe/root", "ok/nested"); err != nil {
		t.Fatalf("legitimate nested path rejected: %v", err)
	}
}

func writeRawDepArchive(t *testing.T, path string, entries []*tar.Header, bodies map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if b, ok := bodies[h.Name]; ok {
			if _, err := tw.Write(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRejectsEscapingSymlink(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	writeRawDepArchive(t, archive, []*tar.Header{
		{Name: "x", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0o777},
		{Name: "x/victim", Typeflag: tar.TypeReg, Mode: 0o644, Size: 5},
	}, map[string][]byte{"x/victim": []byte("PWNED")})

	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractDepCacheArchive(rf, t.TempDir()); err == nil {
		t.Fatal("escaping symlink was accepted")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("file outside the target was overwritten: %q", data)
	}
}

func TestExtractBoundsDecompression(t *testing.T) {
	orig := depCacheMaxExtractBytes
	depCacheMaxExtractBytes = 1 << 10
	defer func() { depCacheMaxExtractBytes = orig }()

	archive := filepath.Join(t.TempDir(), "bomb.tar.gz")
	big := make([]byte, 64<<10)
	writeRawDepArchive(t, archive, []*tar.Header{
		{Name: "big", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(big))},
	}, map[string][]byte{"big": big})

	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractDepCacheArchive(rf, t.TempDir()); err == nil {
		t.Fatal("oversized archive extracted past the cap")
	}
}

func TestDirKeyIncludesPath(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "go.sum")
	if err := os.WriteFile(lock, []byte("same content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := Dir("service-a/vendor", KeyFromFile("go.sum"))
	b := Dir("service-b/vendor", KeyFromFile("go.sum"))
	ka, err := deriveDepCacheKey(a.name, a.keyScope, lock)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := deriveDepCacheKey(b.name, b.keyScope, lock)
	if err != nil {
		t.Fatal(err)
	}
	if ka == kb {
		t.Fatalf("same-basename Dir caches collide on key %q", ka)
	}
}

func TestStagedExtractLeavesDirCleanOnFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	truncated := bytes.NewReader([]byte{0x1f, 0x8b, 0x08, 0x00, 0x00})
	if err := extractDepCacheArchiveStaged(truncated, dir); err == nil {
		t.Fatal("truncated archive extracted without error")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("target directory exists after failed restore (err=%v)", err)
	}
}

func TestLocalDepCacheMissSaveHitCycle(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_URL", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")

	backend := selectDepCacheBackend()
	if _, ok := backend.(*localDepCache); !ok {
		t.Fatalf("expected local backend, got %T", backend)
	}

	ctx := context.Background()
	const key = "dep-go-modules-test-amd64-0011223344556677"

	hit, err := backend.exists(ctx, key)
	if err != nil {
		t.Fatalf("exists on empty store: %v", err)
	}
	if hit {
		t.Fatal("phantom hit on empty store")
	}

	src := t.TempDir()
	populateDepCacheFixture(t, src)
	size, err := backend.store(ctx, key, src)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if size <= 0 {
		t.Fatalf("stored size %d", size)
	}

	hit, err = backend.exists(ctx, key)
	if err != nil || !hit {
		t.Fatalf("exists after store: hit=%v err=%v", hit, err)
	}

	dest := t.TempDir()
	got, err := backend.fetch(ctx, key, dest)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != size {
		t.Fatalf("fetch size %d != stored size %d", got, size)
	}
	assertDepCacheFixture(t, dest)
}

func newCacheServiceStub(t *testing.T, token string) (*httptest.Server, *sync.Map) {
	t.Helper()
	var blobs sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/cache/")
		if !depCacheKeyRE.MatchString(key) {
			http.Error(w, "invalid cache key", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodHead:
			if _, ok := blobs.Load(key); !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			v, ok := blobs.Load(key)
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(v.([]byte))
		case http.MethodPut:
			data := make([]byte, 0, 1<<20)
			buf := make([]byte, 32<<10)
			for {
				n, err := r.Body.Read(buf)
				data = append(data, buf[:n]...)
				if err != nil {
					break
				}
			}
			blobs.Store(key, data)
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "GET, HEAD, or PUT only", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &blobs
}

func TestRemoteDepCacheMissSaveHitCycle(t *testing.T) {
	srv, blobs := newCacheServiceStub(t, "sekrit")
	t.Setenv("SPARKWING_CACHE_URL", srv.URL)
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv("SPARKWING_CACHE_TOKEN", "")
	t.Setenv("SPARKWING_AGENT_TOKEN", "sekrit")

	backend := selectDepCacheBackend()
	if _, ok := backend.(*remoteDepCache); !ok {
		t.Fatalf("expected remote backend, got %T", backend)
	}

	ctx := context.Background()
	const key = "dep-node-modules-linux-arm64-8899aabbccddeeff"

	hit, err := backend.exists(ctx, key)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if hit {
		t.Fatal("phantom hit")
	}

	src := t.TempDir()
	populateDepCacheFixture(t, src)
	size, err := backend.store(ctx, key, src)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	if _, ok := blobs.Load(key); !ok {
		t.Fatal("server did not receive the blob")
	}

	hit, err = backend.exists(ctx, key)
	if err != nil || !hit {
		t.Fatalf("exists after store: hit=%v err=%v", hit, err)
	}

	dest := t.TempDir()
	got, err := backend.fetch(ctx, key, dest)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != size {
		t.Fatalf("fetch size %d != stored %d", got, size)
	}
	assertDepCacheFixture(t, dest)
}

func TestRemoteDepCacheOversizeArchiveSkipsUpload(t *testing.T) {
	srv, blobs := newCacheServiceStub(t, "")
	backend := &remoteDepCache{baseURL: srv.URL}

	prev := remoteDepCacheMaxBytes
	remoteDepCacheMaxBytes = 64
	t.Cleanup(func() { remoteDepCacheMaxBytes = prev })

	src := t.TempDir()
	populateDepCacheFixture(t, src)
	_, err := backend.store(context.Background(), "dep-big-linux-amd64-0000000000000000", src)
	if err == nil {
		t.Fatal("oversize store did not report an error")
	}
	if !strings.Contains(err.Error(), "not uploading") {
		t.Fatalf("error does not describe the size skip: %v", err)
	}
	var uploaded int
	blobs.Range(func(_, _ any) bool { uploaded++; return true })
	if uploaded != 0 {
		t.Fatalf("oversize archive reached the server (%d blobs)", uploaded)
	}
}

func TestRemoteDepCacheRejectedAuthSurfacesAsError(t *testing.T) {
	srv, _ := newCacheServiceStub(t, "right-token")
	backend := &remoteDepCache{baseURL: srv.URL, token: "wrong-token"}
	if _, err := backend.exists(context.Background(), "dep-x-linux-amd64-0000000000000000"); err == nil {
		t.Fatal("401 did not surface as an error")
	}
}

func setDepCacheWorkdir(t *testing.T, dir string) {
	t.Helper()
	prev := CurrentRuntime().WorkDir
	SetWorkDir(dir)
	t.Cleanup(func() { SetWorkDir(prev) })
}

func TestDirCacheRunNeverSavesOnFailure(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_URL", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")

	work := t.TempDir()
	setDepCacheWorkdir(t, work)
	if err := os.WriteFile(filepath.Join(work, "Gemfile.lock"), []byte("GEM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	depDir := filepath.Join(work, "vendor", "bundle")

	st := &dirCacheRun{spec: Dir("vendor/bundle", KeyFromFile("Gemfile.lock")), node: "t"}
	if err := st.restore(context.Background()); err != nil {
		t.Fatalf("restore returned error (must be best-effort nil): %v", err)
	}
	if !st.missed {
		t.Fatal("expected a miss on an empty store")
	}

	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "gem.rb"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	st.save(context.Background(), fmt.Errorf("node exploded"))

	backend := selectDepCacheBackend()
	hit, err := backend.exists(context.Background(), st.key)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("cache saved despite node failure")
	}

	st.save(context.Background(), nil)
	hit, err = backend.exists(context.Background(), st.key)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("cache not saved after success")
	}
}

func TestDirCacheRunNpmStoreMissSaveHitCycle(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_URL", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")

	work := t.TempDir()
	setDepCacheWorkdir(t, work)
	if err := os.WriteFile(filepath.Join(work, "package-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	store1 := filepath.Join(t.TempDir(), "npm-cache-1")
	t.Setenv("npm_config_cache", store1)

	first := &dirCacheRun{spec: NpmCache(), node: "t"}
	if err := first.restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !first.missed {
		t.Fatal("expected first-run miss")
	}

	populateDepCacheFixture(t, store1)
	first.save(context.Background(), nil)

	store2 := filepath.Join(t.TempDir(), "npm-cache")
	t.Setenv("npm_config_cache", store2)
	second := &dirCacheRun{spec: NpmCache(), node: "t"}
	if err := second.restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if second.missed {
		t.Fatal("expected second-run hit")
	}
	assertDepCacheFixture(t, store2)
}

func TestDirCacheRunMissingLockfileDisables(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	work := t.TempDir()
	setDepCacheWorkdir(t, work)

	st := &dirCacheRun{spec: GoModules(), node: "t"}
	if err := st.restore(context.Background()); err != nil {
		t.Fatalf("restore must not error on missing lockfile: %v", err)
	}
	if !st.disabled {
		t.Fatal("missing lockfile did not disable the cache")
	}
	st.save(context.Background(), nil)
}

func TestCacheDirRegistersHooksAndSpecs(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "test", func(ctx context.Context) error { return nil })

	n.CacheDir(GoModules(), NpmCache())

	if got := len(n.DirCaches()); got != 2 {
		t.Fatalf("DirCaches len = %d, want 2", got)
	}
	if got := len(n.BeforeRunHooks()); got != 2 {
		t.Fatalf("BeforeRunHooks len = %d, want 2", got)
	}
	if got := len(n.AfterRunHooks()); got != 2 {
		t.Fatalf("AfterRunHooks len = %d, want 2", got)
	}
}

func TestCacheDirPanicsOnStructuralMisuse(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "test", func(ctx context.Context) error { return nil })

	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s did not panic", name)
			}
		}()
		fn()
	}
	assertPanics("empty path", func() { n.CacheDir(Dir("", KeyFromFile("x.lock"))) })
	assertPanics("no key file", func() { n.CacheDir(Dir("vendor", KeySource{})) })
}
