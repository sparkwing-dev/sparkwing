package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestLocalControllerKeySetupRefusalRetainsConfigurationRepair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY", "")
	t.Setenv("SPARKWING_SECRETS_PREVIOUS_KEY", "")
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", "")
	cfg := localcontroller.Config{Bind: bindLocalController}
	owner, err := cfg.Open(t.Context(), home)
	if err != nil {
		t.Fatalf("new home did not start without key setup: %v", err)
	}
	key := filepath.Join(home, "secrets.key")
	info, err := os.Stat(key)
	if err != nil {
		t.Fatalf("new controller did not create its home key: %v", err)
	}
	if err := fssecure.VerifyPrivateConfig(key, info); err != nil {
		t.Fatalf("new controller did not create a private home key: %v", err)
	}
	var refusal *apierr.Error
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(key, key+".saved"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Rename(key+".saved", key) }()
	owner, err = cfg.Open(t.Context(), home)
	if owner != nil {
		_ = owner.Close()
	}
	if !errors.As(err, &refusal) || refusal.Code != apierr.BadRequest || (!strings.Contains(strings.ToLower(refusal.Message+refusal.Hint), "restore") || !strings.Contains(refusal.Message+refusal.Hint, key) || !strings.Contains(refusal.Message+refusal.Hint, "SPARKWING_SECRETS_KEY_FILE")) {
		t.Fatalf("sealed store without its key lost the restore repair: %v", err)
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Fatalf("missing key was replaced before restoring the original: %v", err)
	}
	if err := os.WriteFile(key, bytes.Repeat([]byte{5}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err = cfg.Open(t.Context(), home)
	if owner != nil {
		_ = owner.Close()
	}
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Message+refusal.Hint, key) || !strings.Contains(refusal.Message+refusal.Hint, "SPARKWING_SECRETS_KEY_FILE") {
		t.Fatalf("wrong key lost its named repair: %v", err)
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(key+".saved", key); err != nil {
		t.Fatal(err)
	}
	owner, err = cfg.Open(t.Context(), home)
	if err != nil {
		t.Fatalf("restoring the original key did not reopen the sealed store: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

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

func TestRedirectedLegacyHomeNamesBothKeysAndExactRepair(t *testing.T) {
	user := t.TempDir()
	home := filepath.Join(user, "redirected")
	legacy := filepath.Join(user, "config", "sparkwing", "secrets.key")
	t.Setenv("HOME", user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY", "")
	t.Setenv("SPARKWING_SECRETS_PREVIOUS_KEY", "")
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", legacy)
	cfg := localcontroller.Config{Bind: bindLocalController}
	owner, err := cfg.Open(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", "")
	owner, err = cfg.Open(t.Context(), home)
	if owner != nil {
		_ = owner.Close()
	}
	var refusal *apierr.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("redirected home did not refuse the unselected key: %v", err)
	}
	repair := refusal.Message + refusal.Hint
	for _, want := range []string{filepath.Join(home, "secrets.key"), legacy, "SPARKWING_SECRETS_KEY_FILE=" + legacy} {
		if !strings.Contains(repair, want) {
			t.Fatalf("refusal lacks %q: %s", want, repair)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "secrets.key")); !os.IsNotExist(err) {
		t.Fatalf("refusal created a replacement key: %v", err)
	}
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", legacy)
	owner, err = cfg.Open(t.Context(), home)
	if err != nil {
		t.Fatalf("suggested repair did not reopen the home: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatalf("repair changed the existing key: %v", err)
	}
}

func TestLocalControllerKeyFollowsCapturedHome(t *testing.T) {
	user := t.TempDir()
	ambient := filepath.Join(user, "ambient")
	selected := filepath.Join(user, "selected")
	t.Setenv("HOME", user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("SPARKWING_HOME", ambient)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(selected, "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", "")
	t.Setenv("SPARKWING_SECRETS_KEY", "")
	t.Setenv("SPARKWING_SECRETS_PREVIOUS_KEY", "")
	owner, err := (localcontroller.Config{Bind: bindLocalController}).Open(t.Context(), selected)
	if err != nil {
		t.Fatalf("captured home could not create its key: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(selected, "secrets.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ambient, "secrets.key")); !os.IsNotExist(err) {
		t.Fatalf("changed ambient home's key: %v", err)
	}
	if os.Getenv("SPARKWING_HOME") != ambient {
		t.Fatal("changed ambient home selector")
	}
}
