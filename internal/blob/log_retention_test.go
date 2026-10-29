package blob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLogClassDays(t *testing.T) {
	for _, days := range []int{1, 30, 90, 120, int(^uint(0) >> 1)} {
		if got, ok := LogClassDays("r" + strconv.Itoa(days)); !ok || got != days {
			t.Fatalf("class %d = %d, %v", days, got, ok)
		}
	}
	for _, class := range []string{"", "r", "r0", "r01", "r+1", "r-1", "r1/", "r 1", "r1.0", "R30", "r99999999999999999999999"} {
		if _, ok := LogClassDays(class); ok {
			t.Errorf("accepted class %q", class)
		}
	}
}

func TestFilesystemLogRetention(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	backend, err := NewFilesystem(FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("x", 32)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if n, err := backend.ExpireLogs(t.Context()); n != 0 || err != nil {
		t.Fatalf("empty sweep = %d, %v", n, err)
	}
	var retained, expired []string
	for _, class := range []string{"", "r0", "r01", "r+30", "r-1"} {
		if _, ok := LogClassDays(class); ok {
			t.Fatalf("accepted invalid class %q", class)
		}
	}
	for _, days := range []int{30, 90, 120} {
		if got, ok := LogClassDays("r" + strconv.Itoa(days)); !ok || got != days {
			t.Fatalf("class r%d = %d, %t", days, got, ok)
		}
		for i, age := range []time.Duration{time.Duration(days)*24*time.Hour - time.Nanosecond, time.Duration(days) * 24 * time.Hour, time.Duration(days+1) * 24 * time.Hour} {
			for _, name := range []string{"seg-000001.ndjson.gz", "manifest.json"} {
				key := fmt.Sprintf("logs/r%d/teams/acme/runs/run-%d/units/plan/g1/%s", days, i, name)
				u, _, err := backend.SignLogPut(t.Context(), key, 9, digest("log bytes"), now.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				w := httptest.NewRecorder()
				backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, u, strings.NewReader("log bytes")))
				if w.Code != http.StatusCreated {
					t.Fatalf("upload = %d: %s", w.Code, w.Body)
				}
				file := filepath.Join(dir, filepath.FromSlash(key))
				created := now.Add(-age)
				if err := os.Chtimes(file, created, created); err != nil {
					t.Fatal(err)
				}
				w = httptest.NewRecorder()
				backend.ServeHTTP(w, httptest.NewRequest(http.MethodPut, u, strings.NewReader("log bytes")))
				if w.Code != http.StatusCreated {
					t.Fatalf("identical retry = %d: %s", w.Code, w.Body)
				}
				if i == 0 {
					retained = append(retained, file)
				} else {
					expired = append(expired, file)
				}
			}
		}
	}
	// safety: retention applies to log objects only, even inside a private data directory.
	other := filepath.Join(dir, "teams", "acme", "cas", string(digest("data")))
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.AddDate(-1, 0, 0)
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	retained = append(retained, other)
	for i := range 2 {
		n, err := backend.ExpireLogs(t.Context())
		if want := len(expired) * (1 - i); n != want || err != nil {
			t.Fatalf("sweep %d = %d, %v; want %d", i, n, err, want)
		}
	}
	for _, file := range expired {
		if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expired object %s: %v", file, err)
		}
	}
	for _, file := range retained {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("retained object %s: %v", file, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if n, err := backend.ExpireLogs(ctx); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sweep = %d, %v", n, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ExpireLogs(t.Context()); err == nil {
		t.Fatal("closed directory appeared to sweep successfully")
	}
}

func TestFilesystemLogRetentionPreservesUnrecognizedFiles(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewFilesystem(FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	keys := []string{
		"logs/r30/teams/acme/runs/run-1/units/plan/g1/seg-000001.ndjson.gz-uploading",
		"logs/r30/teams/acme/runs/run-1/units/plan/g1/unrecognized",
		"logs/r+1/teams/acme/runs/run-1/units/plan/g1/manifest.json",
		"logs/r" + strconv.Itoa(int(^uint(0)>>1)) + "/teams/acme/runs/run-1/units/plan/g1/manifest.json",
	}
	for _, key := range keys {
		file := filepath.Join(dir, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("preserved"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().AddDate(-1, 0, 0)
		if err := os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := backend.ExpireLogs(t.Context()); n != 0 || err != nil {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	for _, key := range keys {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(key))); err != nil {
			t.Errorf("preserved object %s: %v", key, err)
		}
	}
}
