package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func rotateCount(t *testing.T, base string) int {
	t.Helper()
	resp, err := http.Post(base+"/api/v1/secrets/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST rotate status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Rotated int `json:"rotated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode rotate body: %v", err)
	}
	return body.Rotated
}

func secretValue(t *testing.T, base, path string) string {
	t.Helper()
	status, body := getSecretStatus(t, base+path)
	if status != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, status, body)
	}
	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Value
}

func TestSecrets_PreviousKeyOpensValuesSealedUnderIt(t *testing.T) {
	oldKey, _ := secrets.GenerateKey()
	newKey, _ := secrets.GenerateKey()
	oldCipher, _ := secrets.NewCipher(oldKey)

	rotating, err := secrets.NewCipherWithPrevious(newKey, oldKey)
	if err != nil {
		t.Fatalf("NewCipherWithPrevious: %v", err)
	}
	srv, st := newSecretsTestServer(t, rotating)

	sealed, err := oldCipher.SealBound("TOKEN", "", false, true, "supersecret")
	if err != nil {
		t.Fatalf("SealBound: %v", err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "TOKEN", Value: sealed, Principal: "admin", Masked: true,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}

	if got := secretValue(t, srv.URL, "/api/v1/secrets/TOKEN"); got != "supersecret" {
		t.Fatalf("Value = %q, want supersecret", got)
	}

	currentOnly, _ := secrets.NewCipher(newKey)
	row, err := st.GetSecret("TOKEN")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if _, err := currentOnly.OpenBound("TOKEN", "", false, true, row.Value); err == nil {
		t.Fatal("the stored value already opens under the current key alone; the fallback proved nothing")
	}
}

func TestSecrets_RotateReencryptsUnderTheCurrentKey(t *testing.T) {
	oldKey, _ := secrets.GenerateKey()
	newKey, _ := secrets.GenerateKey()
	oldCipher, _ := secrets.NewCipher(oldKey)

	rotating, err := secrets.NewCipherWithPrevious(newKey, oldKey)
	if err != nil {
		t.Fatalf("NewCipherWithPrevious: %v", err)
	}
	srv, st := newSecretsTestServer(t, rotating)

	rows := []struct {
		name, repo, value string
		shared, masked    bool
	}{
		{name: "TOKEN", value: "supersecret", masked: true},
		{name: "TOKEN", repo: "acme/web", value: "repo-secret", masked: true},
		{name: "REGION", value: "us-east-1", shared: true},
	}
	for _, row := range rows {
		sealed, serr := oldCipher.SealBound(row.name, row.repo, row.shared, row.masked, row.value)
		if serr != nil {
			t.Fatalf("SealBound(%s/%s): %v", row.name, row.repo, serr)
		}
		if err := st.CreateOrReplaceSecret(store.Secret{
			Name: row.name, Value: sealed, Principal: "admin",
			Repo: row.repo, Masked: row.masked, Shared: row.shared,
		}, time.Now().UTC()); err != nil {
			t.Fatalf("CreateOrReplaceSecret(%s/%s): %v", row.name, row.repo, err)
		}
	}

	if got := rotateCount(t, srv.URL); got != len(rows) {
		t.Fatalf("rotated = %d, want %d", got, len(rows))
	}

	currentOnly, _ := secrets.NewCipher(newKey)
	for _, row := range rows {
		stored, err := st.GetSecretRow(row.name, row.repo)
		if err != nil {
			t.Fatalf("GetSecretRow(%s/%s): %v", row.name, row.repo, err)
		}
		plain, oerr := currentOnly.OpenBound(row.name, row.repo, row.shared, row.masked, stored.Value)
		if oerr != nil {
			t.Fatalf("secret %s/%s does not open under the current key alone: %v", row.name, row.repo, oerr)
		}
		if plain != row.value {
			t.Fatalf("secret %s/%s = %q, want %q", row.name, row.repo, plain, row.value)
		}
	}
	if got := secretValue(t, srv.URL, "/api/v1/secrets/TOKEN"); got != "supersecret" {
		t.Fatalf("Value after rotation = %q, want supersecret", got)
	}
}

func TestSecrets_RotateSealsValuesHeldAsPlaintext(t *testing.T) {
	key, _ := secrets.GenerateKey()
	cipher, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, cipher)

	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "LEGACY", Value: "plain-value", Principal: "admin", Masked: true,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}

	if got := rotateCount(t, srv.URL); got != 1 {
		t.Fatalf("rotated = %d, want 1", got)
	}
	row, err := st.GetSecret("LEGACY")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if !secrets.IsBound(row.Value) {
		t.Fatalf("stored value = %q, want a bound envelope", row.Value)
	}
	if strings.Contains(row.Value, "plain-value") {
		t.Fatalf("stored value leaks plaintext: %q", row.Value)
	}
	if got := secretValue(t, srv.URL, "/api/v1/secrets/LEGACY"); got != "plain-value" {
		t.Fatalf("Value after rotation = %q, want plain-value", got)
	}
}

func TestSecrets_RotateRefusedWithoutAKey(t *testing.T) {
	srv, st := newSecretsTestServer(t, nil)
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "TOKEN", Value: "plain-value", Principal: "admin", Masked: true,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/v1/secrets/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST rotate without a key status = %d, want 400", resp.StatusCode)
	}
	row, err := st.GetSecret("TOKEN")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if row.Value != "plain-value" {
		t.Fatalf("refused rotation changed the stored value to %q", row.Value)
	}
}

func TestSecrets_RotateLeavesEveryRowWhenOneDoesNotOpen(t *testing.T) {
	key, _ := secrets.GenerateKey()
	strayKey, _ := secrets.GenerateKey()
	cipher, _ := secrets.NewCipher(key)
	stray, _ := secrets.NewCipher(strayKey)
	srv, st := newSecretsTestServer(t, cipher)

	good, _ := cipher.SealBound("GOOD", "", false, true, "readable")
	bad, _ := stray.SealBound("BAD", "", false, true, "unreachable")
	for name, value := range map[string]string{"GOOD": good, "BAD": bad} {
		if err := st.CreateOrReplaceSecret(store.Secret{
			Name: name, Value: value, Principal: "admin", Masked: true,
		}, time.Now().UTC()); err != nil {
			t.Fatalf("CreateOrReplaceSecret(%s): %v", name, err)
		}
	}

	resp, err := http.Post(srv.URL+"/api/v1/secrets/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("POST rotate status = %d, want 500", resp.StatusCode)
	}
	if strings.Contains(string(body[:n]), "readable") {
		t.Fatalf("failed rotation leaked a secret value: %s", body[:n])
	}

	row, err := st.GetSecret("GOOD")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if row.Value != good {
		t.Fatal("a rotation that could not finish still rewrote a row")
	}
}
