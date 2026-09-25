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
	"sync/atomic"
	"testing"
)

func TestDirectBinaryMissNeverReadsLegacyCache(t *testing.T) {
	var legacyReads atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyReads.Add(1)
		http.Error(w, "poison", http.StatusOK)
	}))
	defer legacy.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/data/capabilities" {
			_, _ = io.WriteString(w, `{"download":true,"upload":true}`)
			return
		}
		if r.URL.Path == "/api/v1/data/download" {
			http.NotFound(w, r)
			return
		}
		t.Errorf("unexpected route %s", r.URL.Path)
	}))
	defer controller.Close()
	err := TryBinaryPreferred(context.Background(), controller.URL, "runner", "grant", "run", legacy.URL,
		"01234567-89abcdef", filepath.Join(t.TempDir(), "binary"))
	if !errors.Is(err, ErrMiss) || legacyReads.Load() != 0 {
		t.Fatalf("direct miss = %v, legacy reads = %d", err, legacyReads.Load())
	}
}

func TestDirectBinaryUploadSendsNoBearerToObjectStore(t *testing.T) {
	body := []byte("binary bytes")
	sum := sha256.Sum256(body)
	digest := base64.StdEncoding.EncodeToString(sum[:])
	var uploaded, committed atomic.Bool
	object := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("object store received bearer %q", got)
		}
		if got := r.Header.Get("x-amz-checksum-sha256"); got != digest {
			t.Errorf("checksum = %q", got)
		}
		got, _ := io.ReadAll(r.Body)
		if string(got) != string(body) {
			t.Errorf("uploaded bytes = %q", got)
		}
		uploaded.Store(true)
	}))
	defer object.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/data/capabilities":
			_, _ = io.WriteString(w, `{"download":true,"upload":true}`)
		case "/api/v1/data/upload":
			if got := r.Header.Get("Authorization"); got != "Bearer grant" {
				t.Errorf("reserve bearer = %q", got)
			}
			var req struct {
				Key   string `json:"key"`
				RunID string `json:"run_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Key != "bin/01234567-89abcdef/"+fmt.Sprintf("%x", sum) || req.RunID != "run" {
				t.Errorf("reserve = %+v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id": "u", "url": object.URL,
				"headers": map[string]string{"x-amz-checksum-sha256": digest},
			})
		case "/api/v1/data/commit":
			if !uploaded.Load() {
				t.Error("commit preceded PUT")
			}
			committed.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	src := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(src, body, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := UploadBinaryPreferred(context.Background(), controller.URL, "grant", "run", "", "01234567-89abcdef", src); err != nil || !committed.Load() {
		t.Fatalf("upload = %v, committed = %v", err, committed.Load())
	}
}
