package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/v2/secrets"
)

// End-to-end check that the secret POST/GET round-trip with a
// configured cipher stores ciphertext on disk but returns the
// original value to authorized readers. Also covers the
// backward-compat path: cipher present, row written before cipher
// landed.

func newSecretsTestServer(t *testing.T, c *secrets.Cipher) (*httptest.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(controller.New(st, nil).WithSecretsCipher(c).Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

func postSecretJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func TestSecrets_EncryptionRoundTrip(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)

	// Write
	resp := postSecretJSON(t, srv.URL+"/api/v1/secrets",
		map[string]string{"name": "TOKEN", "value": "supersecret"})
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST status = %d, body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()

	// Direct store read: must NOT contain the plaintext.
	row, err := st.GetSecret("TOKEN")
	if err != nil {
		t.Fatalf("store.GetSecret: %v", err)
	}
	if !secrets.IsEncrypted(row.Value) {
		t.Fatalf("on-disk value lacks envelope prefix: %q", row.Value)
	}
	if strings.Contains(row.Value, "supersecret") {
		t.Fatalf("on-disk value leaks plaintext: %q", row.Value)
	}

	// HTTP read: must return the plaintext.
	resp, err = http.Get(srv.URL + "/api/v1/secrets/TOKEN")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != "supersecret" {
		t.Fatalf("Value = %q, want supersecret", got.Value)
	}
}

func TestSecrets_BackwardCompatPlaintextRow(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)

	// Simulate a row that predates the cipher: write directly via
	// the store with no envelope prefix. The handler with a cipher
	// configured should still serve it correctly.
	if err := st.CreateOrReplaceSecret("LEGACY", "old-plaintext", "test", true, time.Now()); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/secrets/LEGACY")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != "old-plaintext" {
		t.Fatalf("Value = %q, want old-plaintext (legacy row)", got.Value)
	}
}

func TestSecrets_NoCipherStoresPlaintext(t *testing.T) {
	srv, st := newSecretsTestServer(t, nil)

	resp := postSecretJSON(t, srv.URL+"/api/v1/secrets",
		map[string]string{"name": "TOKEN", "value": "abc"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	row, err := st.GetSecret("TOKEN")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if secrets.IsEncrypted(row.Value) {
		t.Fatal("no cipher configured but value was encrypted")
	}
	if row.Value != "abc" {
		t.Fatalf("on-disk = %q, want abc", row.Value)
	}
}

// Sanity: CreateRun is needed by some other tests in this package; we
// don't use it here, but the import bookkeeping must reference store.
var _ = context.Background
