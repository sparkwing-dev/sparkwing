package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
)

func TestLocalObjectURLsSurviveControllerRestart(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY", "")
	keyFile := filepath.Join(t.TempDir(), "secrets.key")
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", keyFile)
	home := t.TempDir()
	var db *store.Store
	var services localcontroller.Services
	cfg := localcontroller.Config{Bind: func(ctx context.Context, b localcontroller.Binding) (localcontroller.Services, error) {
		var err error
		db = b.Store
		services, err = bindLocalController(ctx, b)
		return services, err
	}}
	owner, err := cfg.Open(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{})
	if err != nil {
		t.Fatal(err)
	}
	key, err := db.AsOperator().LocalObjectSigningKey(t.Context(), ring.For(db.AsOperator()))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: filepath.Join(home, "data"), Endpoint: "http://sparkwing-api", SigningKey: key})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("hello"))
	digest := hex.EncodeToString(sum[:])
	expires := time.Now().Add(time.Minute)
	put, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: "teams/local/pending/restart", Size: 5, SHA256: digest, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	services.HTTP.API.ServeHTTP(response, httptest.NewRequest(http.MethodPut, put.URL, strings.NewReader("hello")))
	if response.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", response.Code, response.Body)
	}
	object := blob.Blob{Team: "local", Digest: blob.Digest(digest), Size: 5}
	if err := objects.CommitObject(t.Context(), "teams/local/pending/restart", object); err != nil {
		t.Fatal(err)
	}
	final, err := blob.ObjectKey("local", object.Digest)
	if err != nil {
		t.Fatal(err)
	}
	get, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: final, Size: 5, SHA256: digest, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	if err := objects.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		owner, err = cfg.Open(t.Context(), home)
		if err != nil {
			t.Fatal(err)
		}
		response = httptest.NewRecorder()
		services.HTTP.API.ServeHTTP(response, httptest.NewRequest(http.MethodGet, get.URL, nil))
		if response.Code != http.StatusOK || response.Body.String() != "hello" {
			t.Fatalf("restarted GET: %d %s", response.Code, response.Body)
		}
		response = httptest.NewRecorder()
		services.HTTP.API.ServeHTTP(response, httptest.NewRequest(http.MethodPut, put.URL, strings.NewReader("hello")))
		if response.Code != http.StatusCreated {
			t.Fatalf("restarted PUT: %d %s", response.Code, response.Body)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, key) || bytes.Contains(raw, []byte(hex.EncodeToString(key))) {
		t.Fatal("signing key persisted in plaintext")
	}
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	owner, err = cfg.Open(t.Context(), home)
	if err == nil {
		_ = owner.Close()
		t.Fatal("controller replaced lost signing key")
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatal("lost key was recreated")
	}
}
