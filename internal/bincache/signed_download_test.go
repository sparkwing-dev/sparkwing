package bincache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTryBinaryPreferSigned(t *testing.T) {
	payload := []byte("signed binary")
	sum := sha256.Sum256(payload)
	var downloads, legacy int
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads++
		if r.Header.Get("Authorization") != "" {
			t.Error("bearer sent to blob host")
		}
		_, _ = w.Write(payload)
	}))
	defer blob.Close()
	legacyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { legacy++; w.WriteHeader(http.StatusNotFound) }))
	defer legacyServer.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services":
			_ = json.NewEncoder(w).Encode(map[string]string{"data_download_url": "/api/v1/data/download"})
		case "/api/v1/data/download":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer grant" {
				t.Errorf("signing request %s %q", r.Method, r.Header.Get("Authorization"))
			}
			var body struct{ Kind, Key string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Kind != "binary" || body.Key != "bins/hash" {
				t.Errorf("signing body = %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"url": blob.URL, "sha256": hex.EncodeToString(sum[:]), "size": len(payload), "expires": "2026-09-24T00:00:00Z"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer controller.Close()
	dest := filepath.Join(t.TempDir(), "binary")
	if err := TryBinaryPreferSigned(context.Background(), controller.URL, "runner", "grant", legacyServer.URL, "hash", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("binary = %q, %v", got, err)
	}
	if downloads != 1 || legacy != 0 {
		t.Fatalf("downloads=%d legacy=%d", downloads, legacy)
	}
}

func TestTryBinaryPreferSignedDoesNotFallBackAfterSigningFailure(t *testing.T) {
	legacy := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services":
			_ = json.NewEncoder(w).Encode(map[string]string{"data_download_url": "/api/v1/data/download"})
		case "/api/v1/data/download":
			w.WriteHeader(http.StatusForbidden)
		case "/bin/hash":
			legacy++
		}
	}))
	defer server.Close()
	err := TryBinaryPreferSigned(context.Background(), server.URL, "runner", "grant", server.URL, "hash", filepath.Join(t.TempDir(), "binary"))
	if err == nil || errors.Is(err, ErrMiss) || legacy != 0 {
		t.Fatalf("err=%v legacy=%d", err, legacy)
	}
}

func TestTryBinaryPreferSignedRejectsChangedBytes(t *testing.T) {
	want := sha256.Sum256([]byte("original"))
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tampered")) }))
	defer blob.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services":
			_ = json.NewEncoder(w).Encode(map[string]string{"data_download_url": "/api/v1/data/download"})
		case "/api/v1/data/download":
			_ = json.NewEncoder(w).Encode(map[string]any{"url": blob.URL, "sha256": hex.EncodeToString(want[:]), "size": 8})
		}
	}))
	defer controller.Close()
	dest := filepath.Join(t.TempDir(), "binary")
	if err := TryBinaryPreferSigned(context.Background(), controller.URL, "runner", "grant", "", "hash", dest); !errors.Is(err, ErrDigest) {
		t.Fatalf("err = %v, want ErrDigest", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("unverified binary installed: %v", err)
	}
}

func TestTryBinaryPreferSignedFallsBackWithoutAnnouncement(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer controller.Close()
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer legacy.Close()
	if err := TryBinaryPreferSigned(context.Background(), controller.URL, "runner", "grant", legacy.URL, "hash", filepath.Join(t.TempDir(), "binary")); !errors.Is(err, ErrMiss) {
		t.Fatalf("err = %v, want ErrMiss", err)
	}
}
