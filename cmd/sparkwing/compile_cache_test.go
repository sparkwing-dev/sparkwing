package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/bincache"
)

// fakeCacheServer is a minimal stand-in for sparkwing-cache /bin/<hash>.
// Stores uploads in-memory so tests can assert on them without a
// filesystem or the real server.
type fakeCacheServer struct {
	mu       sync.Mutex
	store    map[string][]byte // hash -> binary bytes
	gotToken map[string]string // hash -> Authorization header seen on PUT
	lastHash string
}

func newFakeCacheServer() *fakeCacheServer {
	return &fakeCacheServer{
		store:    map[string][]byte{},
		gotToken: map[string]string{},
	}
}

var validCacheHash = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{8}){0,3}$`)

func (s *fakeCacheServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bin/") {
			http.NotFound(w, r)
			return
		}
		hash := strings.TrimPrefix(r.URL.Path, "/bin/")
		if !validCacheHash.MatchString(hash) {
			http.Error(w, "bad hash", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lastHash = hash
		switch r.Method {
		case http.MethodGet:
			data, ok := s.store[hash]
			if !ok {
				http.Error(w, "miss", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
		case http.MethodPut:
			s.gotToken[hash] = r.Header.Get("Authorization")
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read err", http.StatusBadRequest)
				return
			}
			s.store[hash] = body
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (s *fakeCacheServer) has(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.store[hash]
	return ok
}

// TestTryRemoteBinary_Hit downloads a stored binary and writes it to
// the destination path with executable bits set.
func TestTryRemoteBinary_Hit(t *testing.T) {
	fake := newFakeCacheServer()
	fake.store["aaaaaaaa-bbbbbbbb"] = []byte("the binary bytes")

	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bin", "pipeline")
	if err := bincache.TryBinary(srv.URL, "aaaaaaaa-bbbbbbbb", dest); err != nil {
		t.Fatalf("hit path returned err: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, []byte("the binary bytes")) {
		t.Errorf("payload mismatch: %q", got)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("destination not executable: %v", info.Mode())
	}
}

// TestTryRemoteBinary_Miss returns the sentinel on 404 so callers
// can distinguish "not yet cached" from "server broken."
func TestTryRemoteBinary_Miss(t *testing.T) {
	fake := newFakeCacheServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	err := bincache.TryBinary(srv.URL, "cafecafe-deadbeef", filepath.Join(t.TempDir(), "bin"))
	if !errors.Is(err, bincache.ErrMiss) {
		t.Errorf("want bincache.ErrMiss, got %v", err)
	}
}

// TestUploadRemoteBinary_Authenticated sends the bearer token on PUT.
func TestUploadRemoteBinary_Authenticated(t *testing.T) {
	fake := newFakeCacheServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "pipeline")
	if err := os.WriteFile(srcPath, []byte("compiled binary body"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := bincache.UploadBinary(srv.URL, "test-token-123", "feedbeef-deadc0de", srcPath); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !fake.has("feedbeef-deadc0de") {
		t.Fatal("fake cache didn't record the upload")
	}
	if got := fake.gotToken["feedbeef-deadc0de"]; got != "Bearer test-token-123" {
		t.Errorf("Authorization header = %q, want Bearer test-token-123", got)
	}
}

// TestPipelineCacheKey_HyphenFormat ensures the hash we send to the
// cache matches the server's validation regex. Caught a real bug the
// first time through: a 16-char continuous hex string was being
// rejected as invalid by the cache.
func TestPipelineCacheKey_HyphenFormat(t *testing.T) {
	dir := scaffoldPipelineRepo(t)
	key, err := bincache.PipelineCacheKey(dir)
	if err != nil {
		t.Fatalf("pipelineCacheKey: %v", err)
	}
	if !validCacheHash.MatchString(key) {
		t.Errorf("key %q does not match cache regex", key)
	}
	if !strings.Contains(key, "-") {
		t.Errorf("key %q missing hyphen separator", key)
	}
}
