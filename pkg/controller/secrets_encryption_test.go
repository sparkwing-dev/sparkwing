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

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newSecretsTestServer(t *testing.T, c controller.Cipher) (*httptest.Server, *store.Store) {
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

	resp := postSecretJSON(t, srv.URL+"/api/v1/secrets",
		map[string]string{"name": "TOKEN", "value": "supersecret"})
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST status = %d, body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()

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

func TestSecrets_EnvelopeIsBoundToNameAndRepo(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)

	resp := postSecretJSON(t, srv.URL+"/api/v1/secrets",
		map[string]string{"name": "TOKEN", "value": "supersecret", "repo": "acme/api"})
	resp.Body.Close()

	row, err := st.GetSecretForRepo("TOKEN", "acme/api")
	if err != nil {
		t.Fatalf("GetSecretForRepo: %v", err)
	}
	if !strings.HasPrefix(row.Value, "enc:v2:") {
		t.Fatalf("stored envelope = %q, want an enc:v2: prefix", row.Value)
	}

	for _, c2 := range []struct {
		label, name, repo, query string
	}{
		{"other name in the same repository", "OTHER", "acme/api", "?repo=acme/api"},
		{"same name in another repository", "TOKEN", "acme/web", "?repo=acme/web"},
		{"same name on the unscoped row", "TOKEN", "", ""},
	} {
		t.Run(c2.label, func(t *testing.T) {
			if err := st.CreateOrReplaceSecret(store.Secret{
				Name: c2.name, Value: row.Value, Principal: "attacker", Repo: c2.repo,
			}, time.Now().UTC()); err != nil {
				t.Fatalf("CreateOrReplaceSecret: %v", err)
			}
			status, body := getSecretStatus(t, srv.URL+"/api/v1/secrets/"+c2.name+c2.query)
			if status != http.StatusInternalServerError {
				t.Fatalf("GET moved envelope status = %d, want 500, body = %s", status, body)
			}
			if strings.Contains(body, "supersecret") {
				t.Fatalf("moved envelope leaked plaintext: %s", body)
			}
		})
	}

	status, body := getSecretStatus(t, srv.URL+"/api/v1/secrets/TOKEN?repo=acme/api")
	if status != http.StatusOK {
		t.Fatalf("GET own row status = %d, body = %s", status, body)
	}
	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != "supersecret" {
		t.Fatalf("Value = %q, want supersecret", got.Value)
	}
}

func TestSecrets_ReadsEnvelopesWrittenBeforeBinding(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)

	legacy, err := c.Seal("older secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "LEGACY", Value: legacy, Principal: "admin",
	}, time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}

	status, body := getSecretStatus(t, srv.URL+"/api/v1/secrets/LEGACY")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", status, body)
	}
	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != "older secret" {
		t.Fatalf("Value = %q, want %q", got.Value, "older secret")
	}
}

func TestSecrets_PlaintextRowFailsClosedOnceEncrypted(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)

	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "PREDATES", Value: "plain value", Principal: "admin",
	}, time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}

	status, body := getSecretStatus(t, srv.URL+"/api/v1/secrets/PREDATES")
	if status != http.StatusInternalServerError {
		t.Fatalf("GET status = %d, want 500, body = %s", status, body)
	}
	if strings.Contains(body, "plain value") {
		t.Fatalf("failed read leaked the stored value: %s", body)
	}
}

func getSecretStatus(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
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

var _ = context.Background
