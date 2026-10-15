package blob_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestFilesystemSignedCommit(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{
		Directory: dir, Endpoint: "https://controller.invalid/objects",
		SigningKey: []byte(strings.Repeat("x", 32)), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	object := blob.Blob{Team: "team", Digest: digest("hello"), Size: 5}
	key := "teams/team/pending/ticket"
	signed, err := backend.Sign(t.Context(), capability.ObjectToSign{
		Method: http.MethodPut, Key: key, Size: 5, SHA256: string(object.Digest), ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, signed.URL, strings.NewReader("hello"))
	response := httptest.NewRecorder()
	backend.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("PUT = %d: %s", response.Code, response.Body)
	}
	if err := backend.CommitObject(t.Context(), key, object); err != nil {
		t.Fatal(err)
	}
	if err := backend.CommitObject(t.Context(), key, object); err != nil {
		t.Fatalf("retry before settlement: %v", err)
	}
	final, err := blob.ObjectKey(object.Team, object.Digest)
	if err != nil {
		t.Fatal(err)
	}
	get, err := backend.Sign(t.Context(), capability.ObjectToSign{
		Method: http.MethodGet, Key: final, Size: 5, SHA256: string(object.Digest), ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	backend.ServeHTTP(response, httptest.NewRequest(http.MethodGet, get.URL, nil))
	if response.Code != http.StatusOK || response.Body.String() != "hello" {
		t.Fatalf("GET = %d: %s", response.Code, response.Body)
	}
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(key))); err != nil {
		t.Fatal(err)
	}
	if err := backend.CommitObject(t.Context(), key, object); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("commit without this ticket's staging: %v", err)
	}
	response = httptest.NewRecorder()
	backend.ServeHTTP(response, httptest.NewRequest(http.MethodPut, signed.URL, strings.NewReader("hello")))
	if response.Code != http.StatusCreated {
		t.Fatalf("new staging PUT = %d: %s", response.Code, response.Body)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(final)), []byte("jello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backend.CommitObject(t.Context(), key, object); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("adopt corrupt object: %v", err)
	}
}

var (
	_ io.Closer             = (*blob.Filesystem)(nil)
	_ capability.DataSigner = (*blob.Filesystem)(nil)
)

func TestFilesystemRefusesInvalidTransfers(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid/objects", SigningKey: []byte(strings.Repeat("x", 32)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	intent := capability.ObjectToSign{Method: http.MethodPut, Key: "teams/team/pending/ticket", SHA256: string(digest("hello")), Size: 5, ExpiresAt: now.Add(time.Minute)}
	signed, err := backend.Sign(t.Context(), intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"hell", "hello!", "jello"} {
		t.Run(body, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, signed.URL, strings.NewReader(body))
			r.ContentLength = -1
			w := httptest.NewRecorder()
			backend.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("PUT = %d", w.Code)
			}
			if _, err := os.Stat(filepath.Join(dir, intent.Key)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bad upload published: %v", err)
			}
		})
	}
	for _, change := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"method", func(r *http.Request) { r.Method = http.MethodGet }},
		{"path", func(r *http.Request) { r.URL.Path = "/elsewhere" }},
		{"signature", func(r *http.Request) { r.URL.RawQuery += "x" }},
		{"duplicate", func(r *http.Request) { r.URL.RawQuery += "&token=extra" }},
		{"tamper", func(r *http.Request) {
			q := r.URL.Query()
			q.Set("token", q.Get("token")+"A")
			r.URL.RawQuery = q.Encode()
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, signed.URL, strings.NewReader("hello"))
			change.mutate(r)
			w := httptest.NewRecorder()
			backend.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("tampered request = %d", w.Code)
			}
		})
	}
	now = intent.ExpiresAt
	w := httptest.NewRecorder()
	backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, signed.URL, strings.NewReader("hello")))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired request = %d", w.Code)
	}
	files, err := os.ReadDir(filepath.Join(dir, "teams/team/pending"))
	if err != nil || len(files) != 0 {
		t.Fatalf("partial upload files: %v, %v", files, err)
	}
}

func TestFilesystemSigningRejectsPathsAndLogs(t *testing.T) {
	now := time.Now()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.invalid/objects", SigningKey: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	for _, key := range []string{"../escape", "teams/team/pending/../escape", "teams/team/pending/..", "teams/team/pending/ticket%2fescape", "teams/team/logs/segment", "teams/../pending/ticket", "teams/team/cas/" + string(digest("hello"))} {
		_, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: key, Size: 5, SHA256: string(digest("hello")), ExpiresAt: now.Add(time.Minute)})
		if err == nil {
			t.Errorf("signed unsafe upload %q", key)
		}
	}
	_, err = backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: "teams/team/logs/segment", Size: 5, SHA256: string(digest("hello")), ExpiresAt: now.Add(time.Minute)})
	if err == nil {
		t.Fatal("signed a log download")
	}
	_, err = backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: "teams/team/cas/" + string(digest("hello")), Size: 5, SHA256: string(digest("hello")), ExpiresAt: now.Add(2 * time.Minute)})
	if err == nil {
		t.Fatal("signed a data download beyond its sixty-second lifetime")
	}
	if err := backend.DeleteObject(t.Context(), "../escape"); err == nil {
		t.Fatal("deleted outside object namespace")
	}
}
