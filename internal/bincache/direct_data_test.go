package bincache

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/directdata"
)

type binaryTransport func(*http.Request) (*http.Response, error)

func (f binaryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func binaryData(handler http.HandlerFunc) *directdata.Client {
	return directdata.New("https://controller.invalid", "capability", "run", &http.Client{
		Transport: binaryTransport(func(r *http.Request) (*http.Response, error) {
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			w := httptest.NewRecorder()
			handler(w, r)
			return w.Result(), nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
}

func TestDownloadBinaryVerifiesSizeAndDigest(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		size       int64
		valid      bool
	}{
		{"valid", "hello", 5, true},
		{"same_size_corrupt", "jello", 5, false},
		{"short", "hello", 6, false},
		{"long", "hello", 4, false},
		{"invalid_size", "hello", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sum := fmt.Sprintf("%x", sha256.Sum256([]byte("hello")))
			data := binaryData(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/data/download":
					_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://objects.invalid/blob", "size": tc.size, "sha256": sum})
				case "/blob":
					if r.Header.Get("Authorization") != "" {
						t.Fatal("object received controller credential")
					}
					_, _ = io.WriteString(w, tc.body)
				default:
					t.Fatalf("unexpected cache request %s", r.URL.Path)
				}
			})
			dir := t.TempDir()
			dest := filepath.Join(dir, "binary")
			err := DownloadBinary(t.Context(), data, "hash", dest)
			if (err == nil) != tc.valid {
				t.Fatalf("download = %v", err)
			}
			if tc.valid {
				if got, err := os.ReadFile(dest); err != nil || string(got) != "hello" {
					t.Fatalf("installed = %q, %v", got, err)
				}
			} else {
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("invalid download published files: %v, %v", entries, err)
				}
			}
		})
	}
}

func TestDownloadBinaryMissAndCancellation(t *testing.T) {
	data := binaryData(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/data/download" {
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
		http.NotFound(w, r)
	})
	if err := DownloadBinary(t.Context(), data, "hash", filepath.Join(t.TempDir(), "binary")); !errors.Is(err, ErrMiss) {
		t.Fatalf("miss = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := DownloadBinary(ctx, data, "hash", filepath.Join(t.TempDir(), "binary")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestFailedBinaryPublicationRemovesStaging(t *testing.T) {
	data := binaryData(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/data/download" {
			_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://objects.invalid/blob", "size": 5, "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("hello")))})
		} else {
			_, _ = io.WriteString(w, "hello")
		}
	})
	parent := t.TempDir()
	dest := filepath.Join(parent, "binary")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := DownloadBinary(t.Context(), data, "hash", dest); err == nil {
		t.Fatal("published over a directory")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "binary" {
		t.Fatalf("staging remains: %v, %v", entries, err)
	}
}

func TestDirectBinaryUploadSendsNoBearerToObjectStore(t *testing.T) {
	const body = "binary bytes"
	sum := sha256.Sum256([]byte(body))
	digest := base64.StdEncoding.EncodeToString(sum[:])
	uploaded, committed := false, false
	data := binaryData(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/data/upload":
			if r.Header.Get("Authorization") != "Bearer capability" {
				t.Fatal("missing controller credential")
			}
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req["key"] != "bin/hash/"+fmt.Sprintf("%x", sum) || req["run_id"] != "run" {
				t.Fatalf("reservation = %v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "ticket", "url": "https://objects.invalid/blob", "headers": map[string]string{"x-amz-checksum-sha256": digest}})
		case "/blob":
			if r.Header.Get("Authorization") != "" || r.Header.Get("x-amz-checksum-sha256") != digest {
				t.Fatalf("object headers = %v", r.Header)
			}
			got, err := io.ReadAll(r.Body)
			if err != nil || string(got) != body {
				t.Fatalf("upload = %q, %v", got, err)
			}
			uploaded = true
		case "/api/v1/data/commit":
			if !uploaded {
				t.Fatal("committed before upload")
			}
			committed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected cache request %s", r.URL.Path)
		}
	})
	src := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(src, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := UploadBinary(t.Context(), data, "hash", src); err != nil || !committed {
		t.Fatalf("upload = %v, committed = %v", err, committed)
	}
}
