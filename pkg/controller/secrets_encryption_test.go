package controller_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func newSecretsTestServer(t *testing.T, c controller.Cipher) (*httptest.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "s.db"))
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

	row, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretRow("TOKEN", "")
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

func TestSecrets_EnvelopeIsBoundToNameAndPipeline(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)

	resp := postSecretJSON(t, srv.URL+"/api/v1/secrets",
		map[string]string{"name": "TOKEN", "value": "supersecret", "pipeline": "deploy-api"})
	resp.Body.Close()

	row, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretRow("TOKEN", "deploy-api")
	if err != nil {
		t.Fatalf("GetSecretRow: %v", err)
	}
	if !strings.HasPrefix(row.Value, secrets.BoundPrefix) {
		t.Fatalf("stored envelope = %q, want a %s prefix", row.Value, secrets.BoundPrefix)
	}

	for _, c2 := range []struct {
		label, name, pipeline, query string
	}{
		{"other name in the same pipeline", "OTHER", "deploy-api", "?pipeline=deploy-api"},
		{"same name in another pipeline", "TOKEN", "deploy-web", "?pipeline=deploy-web"},
		{"same name on the unscoped row", "TOKEN", "", ""},
	} {
		t.Run(c2.label, func(t *testing.T) {
			if err := teststore.SeedSecret(st, store.DefaultTeam, store.Secret{
				Name: c2.name, Value: row.Value, Principal: "attacker", Pipeline: c2.pipeline,
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

	status, body := getSecretStatus(t, srv.URL+"/api/v1/secrets/TOKEN?pipeline=deploy-api")
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

func TestSecrets_ReadRefusesEnvelopesSealedBeforeTeamBinding(t *testing.T) {
	f := newBrowserFixture(t, nil)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	f.h = f.srv.WithSecretsCipher(cipher).Handler()
	legacy, err := cipher.Seal("older secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "LEGACY", Value: legacy, Principal: "alice"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/LEGACY", "", nil)
	if status != http.StatusInternalServerError || strings.Contains(body, "older secret") {
		t.Fatalf("unbound read: HTTP %d %s", status, body)
	}
	row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("LEGACY", "")
	if err != nil || row.Value != legacy {
		t.Fatalf("refused read changed the stored envelope: %v", err)
	}
}

func TestSecrets_PlaintextRowFailsClosedOnceEncrypted(t *testing.T) {
	f := newBrowserFixture(t, nil)
	if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "PREDATES", Value: "plain value", Principal: "alice"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/PREDATES", "", nil)
	var failure apierr.Envelope
	if status != http.StatusInternalServerError || json.Unmarshal([]byte(body), &failure) != nil || failure.Error.Code != apierr.Internal || failure.Error.Message != "request failed" || strings.Contains(body, "plain value") || strings.Contains(body, "not sealed") {
		t.Fatalf("plaintext read: HTTP %d %s", status, body)
	}
	row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("PREDATES", "")
	if err != nil || row.Value != "plain value" {
		t.Fatalf("refused read changed the row: %v", err)
	}
}

func TestSecrets_EnvelopeIsBoundToAccessFields(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)

	resp := postSecretJSON(t, srv.URL+"/api/v1/secrets",
		map[string]any{"name": "TOKEN", "value": "supersecret"})
	resp.Body.Close()

	row, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretRow("TOKEN", "")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if row.Shared || !row.Masked {
		t.Fatalf("row was written shared=%v masked=%v, want shared=false masked=true", row.Shared, row.Masked)
	}

	for _, c2 := range []struct {
		label          string
		shared, masked bool
	}{
		{"shared flipped on", true, true},
		{"masked flipped off", false, false},
	} {
		t.Run(c2.label, func(t *testing.T) {
			edited := *row
			edited.Shared = c2.shared
			edited.Masked = c2.masked
			if err := teststore.SeedSecret(st, store.DefaultTeam, edited, time.Now().UTC()); err != nil {
				t.Fatalf("CreateOrReplaceSecret: %v", err)
			}
			status, body := getSecretStatus(t, srv.URL+"/api/v1/secrets/TOKEN")
			if status != http.StatusInternalServerError {
				t.Fatalf("GET widened row status = %d, want 500, body = %s", status, body)
			}
			if strings.Contains(body, "supersecret") {
				t.Fatalf("widened row leaked plaintext: %s", body)
			}
		})
	}

	if err := teststore.SeedSecret(st, store.DefaultTeam, *row, time.Now().UTC()); err != nil {
		t.Fatalf("restore row: %v", err)
	}
	status, body := getSecretStatus(t, srv.URL+"/api/v1/secrets/TOKEN")
	if status != http.StatusOK {
		t.Fatalf("GET untouched row status = %d, body = %s", status, body)
	}
}

func TestSecrets_ListNamesUnboundRows(t *testing.T) {
	f := newBrowserFixture(t, nil)
	if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "BOUND", "value": "fresh"}); code != http.StatusNoContent {
		t.Fatalf("write: HTTP %d %s", code, body)
	}
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := cipher.Seal("older secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "UNBOUND", Value: legacy, Principal: "alice", Masked: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets", "", nil)
	var list struct {
		Secrets []struct {
			Name  string `json:"name"`
			Bound bool   `json:"bound"`
		} `json:"secrets"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || strings.Contains(body, "older secret") || strings.Contains(body, "fresh") {
		t.Fatalf("list: HTTP %d %s", status, body)
	}
	want := map[string]bool{"BOUND": true, "UNBOUND": false}
	for _, row := range list.Secrets {
		bound, exists := want[row.Name]
		if !exists || row.Bound != bound {
			t.Fatalf("unexpected binding metadata: %+v", row)
		}
		delete(want, row.Name)
	}
	if len(want) != 0 {
		t.Fatalf("list omitted rows: %v", want)
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

func TestSecretsMalformedEnvelopeReadsProperty(t *testing.T) {
	f := newBrowserFixture(t, nil)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	f.h = f.srv.WithSecretsCipher(cipher).Handler()
	property := func(value []byte, legacy bool) bool {
		private := "private-malformed-" + hex.EncodeToString(value)
		stored := private
		if legacy {
			var err error
			stored, err = cipher.Seal(private)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "MALFORMED", Value: stored, Principal: "alice"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/MALFORMED", "", nil)
		row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("MALFORMED", "")
		var failure apierr.Envelope
		return err == nil && row.Value == stored && status == http.StatusInternalServerError && json.Unmarshal([]byte(body), &failure) == nil && failure.Error.Code == apierr.Internal && failure.Error.Message == "request failed" && !strings.Contains(body, private) && !strings.Contains(f.logs.String(), private)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
