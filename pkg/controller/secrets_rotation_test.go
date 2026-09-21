package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type rotateResult struct {
	Rotated int `json:"rotated"`
	Skipped []struct {
		Name     string `json:"name"`
		Pipeline string `json:"pipeline"`
	} `json:"skipped"`
}

func rotateSecrets(t *testing.T, base string) rotateResult {
	t.Helper()
	resp, err := http.Post(base+"/api/v1/secrets/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST rotate status = %d, want 200", resp.StatusCode)
	}
	var body rotateResult
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode rotate body: %v", err)
	}
	return body
}

func rotateCount(t *testing.T, base string) int {
	t.Helper()
	result := rotateSecrets(t, base)
	if len(result.Skipped) != 0 {
		t.Fatalf("rotation skipped %+v, want every row rotated", result.Skipped)
	}
	return result.Rotated
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
		name, pipeline, value string
		shared, masked        bool
	}{
		{name: "TOKEN", value: "supersecret", masked: true},
		{name: "TOKEN", pipeline: "deploy-web", value: "pipeline-secret", masked: true},
		{name: "REGION", value: "us-east-1", shared: true},
	}
	for _, row := range rows {
		sealed, serr := oldCipher.SealBound(row.name, row.pipeline, row.shared, row.masked, row.value)
		if serr != nil {
			t.Fatalf("SealBound(%s/%s): %v", row.name, row.pipeline, serr)
		}
		if err := st.CreateOrReplaceSecret(store.Secret{
			Name: row.name, Value: sealed, Principal: "admin",
			Pipeline: row.pipeline, Masked: row.masked, Shared: row.shared,
		}, time.Now().UTC()); err != nil {
			t.Fatalf("CreateOrReplaceSecret(%s/%s): %v", row.name, row.pipeline, err)
		}
	}

	if got := rotateCount(t, srv.URL); got != len(rows) {
		t.Fatalf("rotated = %d, want %d", got, len(rows))
	}

	currentOnly, _ := secrets.NewCipher(newKey)
	for _, row := range rows {
		stored, err := st.GetSecretRow(row.name, row.pipeline)
		if err != nil {
			t.Fatalf("GetSecretRow(%s/%s): %v", row.name, row.pipeline, err)
		}
		plain, oerr := currentOnly.OpenBound(row.name, row.pipeline, row.shared, row.masked, stored.Value)
		if oerr != nil {
			t.Fatalf("secret %s/%s does not open under the current key alone: %v", row.name, row.pipeline, oerr)
		}
		if plain != row.value {
			t.Fatalf("secret %s/%s = %q, want %q", row.name, row.pipeline, plain, row.value)
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

func TestSecrets_RotateSkipsARowThatOpensUnderNoKey(t *testing.T) {
	oldKey, _ := secrets.GenerateKey()
	newKey, _ := secrets.GenerateKey()
	strayKey, _ := secrets.GenerateKey()
	oldCipher, _ := secrets.NewCipher(oldKey)
	stray, _ := secrets.NewCipher(strayKey)

	rotating, err := secrets.NewCipherWithPrevious(newKey, oldKey)
	if err != nil {
		t.Fatalf("NewCipherWithPrevious: %v", err)
	}
	srv, st := newSecretsTestServer(t, rotating)

	good, _ := oldCipher.SealBound("GOOD", "", false, true, "readable")
	lost, _ := stray.SealBound("LOST", "deploy-web", false, true, "unreachable")
	// safety: a plaintext value that opens with the envelope prefix is indistinguishable from a lost envelope.
	const impostor = "enc:v1:not-really-an-envelope"
	rows := []store.Secret{
		{Name: "GOOD", Value: good, Principal: "admin", Masked: true},
		{Name: "LOST", Value: lost, Principal: "admin", Pipeline: "deploy-web", Masked: true},
		{Name: "IMPOSTOR", Value: impostor, Principal: "admin", Masked: true},
	}
	for _, row := range rows {
		if err := st.CreateOrReplaceSecret(row, time.Now().UTC()); err != nil {
			t.Fatalf("CreateOrReplaceSecret(%s): %v", row.Name, err)
		}
	}

	result := rotateSecrets(t, srv.URL)
	if result.Rotated != 1 {
		t.Fatalf("rotated = %d, want 1", result.Rotated)
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("skipped = %+v, want the two rows that open under no key", result.Skipped)
	}
	skipped := map[string]string{}
	for _, skip := range result.Skipped {
		skipped[skip.Name] = skip.Pipeline
	}
	if pipeline, ok := skipped["LOST"]; !ok || pipeline != "deploy-web" {
		t.Fatalf("skipped = %+v, want LOST named with its pipeline", result.Skipped)
	}
	if _, ok := skipped["IMPOSTOR"]; !ok {
		t.Fatalf("skipped = %+v, want IMPOSTOR named", result.Skipped)
	}

	currentOnly, _ := secrets.NewCipher(newKey)
	rotatedRow, err := st.GetSecret("GOOD")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if plain, oerr := currentOnly.OpenBound("GOOD", "", false, true, rotatedRow.Value); oerr != nil || plain != "readable" {
		t.Fatalf("the readable row did not rotate: value=%q err=%v", plain, oerr)
	}
	for name, want := range map[string]string{"LOST": lost, "IMPOSTOR": impostor} {
		row, rerr := st.GetSecretRow(name, skipped[name])
		if rerr != nil {
			t.Fatalf("GetSecretRow(%s): %v", name, rerr)
		}
		if row.Value != want {
			t.Fatalf("skipped row %s was rewritten to %q", name, row.Value)
		}
	}
}

func TestSecrets_RotateRefusedWithoutTheAdminScope(t *testing.T) {
	key, _ := secrets.GenerateKey()
	cipher, _ := secrets.NewCipher(key)
	st := openBootstrapStore(t)
	now := time.Now().UTC()

	reader, _, err := st.CreateToken("reader", store.TokenKindUser,
		[]string{controller.ScopeSecretsRead}, 0, now)
	if err != nil {
		t.Fatalf("seed reader token: %v", err)
	}
	admin, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("seed admin token: %v", err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "TOKEN", Value: "plain-value", Principal: "admin", Masked: true,
	}, now); err != nil {
		t.Fatalf("CreateOrReplaceSecret: %v", err)
	}
	ts := httptest.NewServer(controller.New(st, nil).
		WithSecretsCipher(cipher).EnableAuthFromStore().Handler())
	defer ts.Close()

	if got := postRotateWithBearer(t, ts.URL, ""); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rotate status = %d, want 401", got)
	}
	if got := postRotateWithBearer(t, ts.URL, reader); got != http.StatusForbidden {
		t.Fatalf("secrets.read rotate status = %d, want 403", got)
	}
	row, err := st.GetSecret("TOKEN")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if row.Value != "plain-value" {
		t.Fatalf("a refused rotation changed the stored value to %q", row.Value)
	}
	if got := postRotateWithBearer(t, ts.URL, admin); got != http.StatusOK {
		t.Fatalf("admin rotate status = %d, want 200", got)
	}
}

func postRotateWithBearer(t *testing.T, base, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/secrets/rotate", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
