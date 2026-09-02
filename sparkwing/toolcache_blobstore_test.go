package sparkwing_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type blobStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newBlobServer(t *testing.T) (*httptest.Server, *blobStore) {
	t.Helper()
	bs := &blobStore{data: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/cache/")
		bs.mu.Lock()
		defer bs.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			bs.data[key] = body
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			v, ok := bs.data[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(v)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, bs
}

func TestLintCacheBlobKey_DiffersForDifferentWorkDirs(t *testing.T) {
	first := t.TempDir()
	useWorkDir(t, first)
	keyA := sparkwing.LintCacheBlobKey()

	second := t.TempDir()
	sparkwing.SetWorkDir(second)
	keyB := sparkwing.LintCacheBlobKey()

	if keyA == keyB {
		t.Fatalf("worktrees %q and %q share blob key %q", first, second, keyA)
	}
}

func TestLintCacheBlobKey_StableForSameWorkDir(t *testing.T) {
	useWorkDir(t, t.TempDir())

	first := sparkwing.LintCacheBlobKey()
	if second := sparkwing.LintCacheBlobKey(); first != second {
		t.Fatalf("key changed between calls: %q then %q", first, second)
	}
}

func TestLintCacheBlobKey_StartsWithLintCache(t *testing.T) {
	useWorkDir(t, t.TempDir())
	key := sparkwing.LintCacheBlobKey()
	if !strings.HasPrefix(key, "lint-cache-") {
		t.Fatalf("key %q does not start with lint-cache-", key)
	}
}

func TestSaveLintCache_EmptyGCURLIsNoop(t *testing.T) {
	useWorkDir(t, t.TempDir())
	n, err := sparkwing.SaveLintCache(context.Background(), "", "")
	if err != nil || n != 0 {
		t.Fatalf("empty gcURL: want (0, nil), got (%d, %v)", n, err)
	}
}

func TestRestoreLintCache_EmptyGCURLIsNoop(t *testing.T) {
	useWorkDir(t, t.TempDir())
	ok, n, err := sparkwing.RestoreLintCache(context.Background(), "")
	if err != nil || ok || n != 0 {
		t.Fatalf("empty gcURL: want (false, 0, nil), got (%v, %d, %v)", ok, n, err)
	}
}

func TestRestoreLintCache_MissReturnsFalse(t *testing.T) {
	srv, _ := newBlobServer(t)
	useWorkDir(t, t.TempDir())

	ok, n, err := sparkwing.RestoreLintCache(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected miss, got hit")
	}
	if n != 0 {
		t.Fatalf("expected 0 bytes on miss, got %d", n)
	}
}

func TestSaveAndRestoreLintCache_RoundTrip(t *testing.T) {
	srv, _ := newBlobServer(t)
	workdir := t.TempDir()
	useWorkDir(t, workdir)

	cacheDir := sparkwing.ToolCacheDir("golangci-lint")
	probe := filepath.Join(cacheDir, "subdir", "probe.json")
	if err := os.MkdirAll(filepath.Dir(probe), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(probe, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cacheDir) })

	saved, err := sparkwing.SaveLintCache(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved == 0 {
		t.Fatalf("save reported 0 bytes")
	}

	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatalf("clear cache: %v", err)
	}

	ok, restored, err := sparkwing.RestoreLintCache(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !ok {
		t.Fatalf("expected hit after save, got miss")
	}
	if restored == 0 {
		t.Fatalf("restore reported 0 bytes")
	}

	if _, statErr := os.Stat(probe); statErr != nil {
		t.Fatalf("probe file missing after restore: %v", statErr)
	}
	got, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("read probe: %v", err)
	}
	if string(got) != `{"version":1}` {
		t.Fatalf("probe content = %q, want {\"version\":1}", got)
	}
}

func TestRestoreLintCache_WorkdirMismatchIsRejected(t *testing.T) {
	srv, bs := newBlobServer(t)

	workdirA := t.TempDir()
	useWorkDir(t, workdirA)

	cacheDirA := sparkwing.ToolCacheDir("golangci-lint")
	probe := filepath.Join(cacheDirA, "probe.json")
	if err := os.WriteFile(probe, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cacheDirA) })

	keyA := sparkwing.LintCacheBlobKey()
	if _, err := sparkwing.SaveLintCache(context.Background(), srv.URL, ""); err != nil {
		t.Fatalf("save: %v", err)
	}

	workdirB := t.TempDir()
	sparkwing.SetWorkDir(workdirB)
	keyB := sparkwing.LintCacheBlobKey()

	if keyA == keyB {
		t.Skip("temp dirs produced the same key (probabilistic collision - re-run)")
	}

	bs.mu.Lock()
	bs.data[keyB] = bs.data[keyA]
	bs.mu.Unlock()

	cacheDirB := sparkwing.ToolCacheDir("golangci-lint")
	t.Cleanup(func() { _ = os.RemoveAll(cacheDirB) })

	_, _, err := sparkwing.RestoreLintCache(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected ErrLintCacheWorkdirMismatch, got nil")
	}
	if !errors.Is(err, sparkwing.ErrLintCacheWorkdirMismatch) {
		t.Fatalf("expected ErrLintCacheWorkdirMismatch, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(cacheDirB, "probe.json")); statErr == nil {
		t.Fatal("probe.json was written despite workdir mismatch")
	}
}

func TestSaveLintCache_SkipsNonRegularFiles(t *testing.T) {
	srv, _ := newBlobServer(t)
	workdir := t.TempDir()
	useWorkDir(t, workdir)

	cacheDir := sparkwing.ToolCacheDir("golangci-lint")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cacheDir) })

	regular := filepath.Join(cacheDir, "regular.json")
	if err := os.WriteFile(regular, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write regular: %v", err)
	}
	link := filepath.Join(cacheDir, "link.json")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	saved, err := sparkwing.SaveLintCache(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("save with symlink in cache dir: %v", err)
	}
	if saved == 0 {
		t.Fatalf("expected bytes saved, got 0")
	}

	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatalf("clear cache: %v", err)
	}

	ok, _, err := sparkwing.RestoreLintCache(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !ok {
		t.Fatalf("expected hit after save, got miss")
	}
	if _, statErr := os.Stat(regular); statErr != nil {
		t.Fatalf("regular file missing after restore: %v", statErr)
	}
}

func TestSaveLintCache_EmptyCacheDirIsNoop(t *testing.T) {
	srv, bs := newBlobServer(t)
	useWorkDir(t, t.TempDir())

	_ = sparkwing.ToolCacheDir("golangci-lint")
	t.Cleanup(func() { _ = os.RemoveAll(sparkwing.ToolCacheDir("golangci-lint")) })

	n, err := sparkwing.SaveLintCache(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("empty cache dir: %v", err)
	}
	if n != 0 {
		t.Fatalf("empty cache dir: expected 0 bytes, got %d", n)
	}
	bs.mu.Lock()
	blobCount := len(bs.data)
	bs.mu.Unlock()
	if blobCount != 0 {
		t.Fatalf("empty cache dir: expected no blobs stored, got %d", blobCount)
	}
}

func TestRestoreLintCache_SendsCacheTokenAsBearer(t *testing.T) {
	useWorkDir(t, t.TempDir())

	var authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("SPARKWING_CACHE_TOKEN", "blob-token")
	if _, _, err := sparkwing.RestoreLintCache(context.Background(), srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authz != "Bearer blob-token" {
		t.Fatalf("Authorization = %q, want %q", authz, "Bearer blob-token")
	}

	authz = ""
	t.Setenv("SPARKWING_CACHE_TOKEN", "")
	if _, _, err := sparkwing.RestoreLintCache(context.Background(), srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authz != "" {
		t.Fatalf("Authorization = %q, want no header", authz)
	}
}
