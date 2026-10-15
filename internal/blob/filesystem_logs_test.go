package blob_test

import (
	"bytes"
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
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestFilesystemLogUploadsAreImmutableAndPutOnly(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("x", 32)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	for _, name := range []string{"seg-000001.ndjson.gz", "manifest.json"} {
		t.Run(name, func(t *testing.T) {
			key := "logs/r90/teams/acme/runs/run-1/jobs/build/a1-g1/" + name
			u, _, err := backend.SignLogPut(t.Context(), key, now.Add(15*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			for i, body := range []string{"first bytes", "first bytes", "other bytes"} {
				w := httptest.NewRecorder()
				backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, u, strings.NewReader(body)))
				want := http.StatusCreated
				if i == 2 {
					want = http.StatusBadRequest
				}
				if w.Code != want {
					t.Fatalf("PUT %q = %d: %s", body, w.Code, w.Body)
				}
			}
			r, err := backend.Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			if err = errors.Join(err, r.Close()); err != nil || string(got) != "first bytes" {
				t.Fatalf("stored = %q, %v", got, err)
			}
			w := httptest.NewRecorder()
			backend.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u, nil))
			if w.Code != http.StatusForbidden {
				t.Fatalf("signed log GET = %d", w.Code)
			}
			now = now.Add(15 * time.Minute)
			w = httptest.NewRecorder()
			backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, u, strings.NewReader("first bytes")))
			if w.Code != http.StatusForbidden {
				t.Fatalf("expired PUT = %d", w.Code)
			}
			files, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(key[:strings.LastIndex(key, "/")])))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if strings.HasPrefix(f.Name(), name+"-") {
					t.Fatalf("leaked temp file %s", f.Name())
				}
			}
		})
	}
}

func TestFilesystemLogSigningRejectsInvalidObjects(t *testing.T) {
	now := time.Now()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("x", 32)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	for _, key := range []string{"teams/acme/cas/" + string(digest("x")), "../escape", "logs/r90/teams/acme/runs/../manifest.json", "logs/r90/teams/acme/runs/run-1/jobs/x/a1-g1/seg-000000.ndjson.gz", "logs/r90/teams/acme/runs/run-1/jobs/x/a1-g1/other", "logs/r90/teams/acme/runs/run-1/jobs/x/a1-g1/manifest.json/../manifest.json", "logs/r90/teams/acme/runs/run-1/jobs/x/a1-g1/seg-1000001.ndjson.gz"} {
		if _, _, err := backend.SignLogPut(t.Context(), key, now.Add(time.Minute)); err == nil {
			t.Errorf("signed %q", key)
		}
		if _, err := backend.Get(t.Context(), key); err == nil {
			t.Errorf("opened %q", key)
		}
	}
	key := "logs/r90/teams/acme/runs/run-1/units/plan/g1/manifest.json"
	for _, expiry := range []time.Time{now, now.Add(-time.Second), now.Add(limits.LogUploadURLTTL + time.Nanosecond)} {
		if _, _, err := backend.SignLogPut(t.Context(), key, expiry); err == nil {
			t.Errorf("signed expiry %v", expiry)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := backend.SignLogPut(ctx, key, now.Add(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sign = %v", err)
	}
	if _, err := backend.Get(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read = %v", err)
	}
}

func TestFilesystemLogUploadBoundsAndCancellation(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	for _, name := range []string{"manifest.json", "seg-000001.ndjson.gz"} {
		key := "logs/r30/teams/acme/runs/run-1/units/trigger/g1/" + name
		u, _, err := backend.SignLogPut(t.Context(), key, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		limit := limits.LogSegmentUploadBytes
		if name == "manifest.json" {
			limit = limits.LogManifestBytes
		}
		for _, declared := range []bool{false, true} {
			r := httptest.NewRequest(http.MethodPut, u, bytes.NewReader(make([]byte, limit+1)))
			if !declared {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			backend.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("oversize %s = %d", name, w.Code)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		w := httptest.NewRecorder()
		backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, u, strings.NewReader("bytes")).WithContext(ctx))
		if w.Code >= 200 && w.Code < 300 {
			t.Fatalf("cancelled upload = %d", w.Code)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(key))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid upload published = %v", err)
		}
	}
}

func TestFilesystemConcurrentLogWritesKeepOnePayload(t *testing.T) {
	dir := t.TempDir()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	key := "logs/r90/teams/acme/runs/run-1/units/eval/job/s1-g2/seg-000001.ndjson.gz"
	u, _, err := backend.SignLogPut(t.Context(), key, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		body string
		code int
	}
	replies := make(chan result, 8)
	var writers sync.WaitGroup
	for i := range cap(replies) {
		writers.Go(func() {
			body := strings.Repeat(string(rune('a'+i%2)), 64)
			w := httptest.NewRecorder()
			backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, u, strings.NewReader(body)))
			replies <- result{body: body, code: w.Code}
		})
	}
	writers.Wait()
	close(replies)
	reader, err := backend.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err = errors.Join(err, reader.Close()); err != nil {
		t.Fatal(err)
	}
	for r := range replies {
		want := http.StatusBadRequest
		if r.body == string(body) {
			want = http.StatusCreated
		}
		if r.code != want {
			t.Fatalf("PUT %q = %d, stored = %q", r.body, r.code, body)
		}
	}
	files, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(key[:strings.LastIndex(key, "/")])))
	if err != nil || len(files) != 1 {
		t.Fatalf("upload files = %v, %v", files, err)
	}
}
