package controller_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type listedSecret struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Pipeline string `json:"pipeline"`
	Masked   bool   `json:"masked"`
	Shared   bool   `json:"shared"`
}

func listSecretsAs(t *testing.T, f *tenancyFixture, auth string) (int, map[string]listedSecret, string) {
	t.Helper()
	code, body := f.do("GET", "/api/v1/secrets", auth, nil)
	if code != http.StatusOK {
		return code, nil, body
	}
	var resp struct {
		Secrets []listedSecret `json:"secrets"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode list: %v: %s", err, body)
	}
	out := map[string]listedSecret{}
	for _, s := range resp.Secrets {
		out[s.Name] = s
	}
	return code, out, body
}

// The dashboard's rules: a masked secret is write-only for every browser
// session, owners included, while an unmasked variable is plain config each
// member reads and only an owner writes.
func TestSecrets_DashboardSessionsWriteSecretsAndReadVariables(t *testing.T) {
	f := newTenancyFixture(t, openSQLiteBindingStore(t))
	masked, unmasked := true, false
	for _, row := range []map[string]any{
		{"name": "DEPLOY_KEY", "value": "hunter2", "shared": true, "masked": masked},
		{"name": "REGION", "value": "us-west-2", "shared": true, "masked": unmasked},
		{"name": "WEB_TOKEN", "value": "line one\nline two", "pipeline": "deploy-web"},
	} {
		if code, body := f.do("POST", "/api/v1/secrets", f.ownerA, row); code != http.StatusNoContent {
			t.Fatalf("owner POST %v = %d: %s", row["name"], code, body)
		}
	}

	for _, who := range []struct{ name, auth string }{
		{"owner", f.ownerA}, {"editor", f.editorA}, {"reader", f.readerA},
	} {
		code, listed, body := listSecretsAs(t, f, who.auth)
		if code != http.StatusOK {
			t.Fatalf("%s list = %d: %s", who.name, code, body)
		}
		if strings.Contains(body, "hunter2") || strings.Contains(body, "line one") {
			t.Errorf("%s list carries a masked value: %s", who.name, body)
		}
		if got := listed["REGION"]; got.Value != "us-west-2" || got.Masked {
			t.Errorf("%s list REGION = %+v, want the variable's value", who.name, got)
		}
		if got := listed["WEB_TOKEN"]; got.Pipeline != "deploy-web" || !got.Masked {
			t.Errorf("%s list WEB_TOKEN = %+v, want masked and scoped to deploy-web", who.name, got)
		}
	}

	// safety: the owner who wrote DEPLOY_KEY is the negative control, so owning it is shown not to read it back.
	code, body := f.do("GET", "/api/v1/secrets/DEPLOY_KEY", f.ownerA, nil)
	if code != http.StatusForbidden || strings.Contains(body, "hunter2") || !strings.Contains(body, "write_only") {
		t.Errorf("owner session GET masked secret = %d: %s, want 403 write_only", code, body)
	}
	code, body = f.do("GET", "/api/v1/secrets/WEB_TOKEN?pipeline=deploy-web", f.ownerA, nil)
	if code != http.StatusForbidden || strings.Contains(body, "line one") {
		t.Errorf("owner session GET pipeline secret = %d: %s, want 403", code, body)
	}
	code, body = f.do("GET", "/api/v1/secrets/REGION", f.ownerA, nil)
	if code != http.StatusOK || !strings.Contains(body, "us-west-2") {
		t.Errorf("owner session GET variable = %d: %s, want its value", code, body)
	}

	for _, who := range []struct{ name, auth string }{
		{"editor", f.editorA}, {"reader", f.readerA},
	} {
		if code, body := f.do("POST", "/api/v1/secrets", who.auth,
			map[string]any{"name": "REGION", "value": "eu-west-1", "shared": true, "masked": unmasked}); code != http.StatusForbidden {
			t.Errorf("%s POST = %d want 403: %s", who.name, code, body)
		}
		if code, body := f.do("DELETE", "/api/v1/secrets/DEPLOY_KEY", who.auth, nil); code != http.StatusForbidden {
			t.Errorf("%s DELETE = %d want 403: %s", who.name, code, body)
		}
	}
	if _, listed, _ := listSecretsAs(t, f, f.ownerA); listed["REGION"].Value != "us-west-2" || listed["DEPLOY_KEY"].Name == "" {
		t.Errorf("a refused write changed team A's rows: %+v", listed)
	}

	if code, body := f.do("DELETE", "/api/v1/secrets/WEB_TOKEN?pipeline=deploy-web", f.ownerA, nil); code != http.StatusNoContent {
		t.Fatalf("owner DELETE = %d: %s", code, body)
	}
	if _, listed, _ := listSecretsAs(t, f, f.ownerA); listed["WEB_TOKEN"].Name != "" {
		t.Errorf("WEB_TOKEN still listed after delete")
	}
}

// A variable is sealed at rest like a secret, so the list opens its envelope
// to show the value.
func TestSecrets_ListOpensAVariablesEnvelope(t *testing.T) {
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	srv, st := newSecretsTestServer(t, c)
	for _, row := range []map[string]any{
		{"name": "REGION", "value": "us-west-2", "masked": false},
		{"name": "TOKEN", "value": "supersecret"},
	} {
		resp := postSecretJSON(t, srv.URL+"/api/v1/secrets", row)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST %v = %d", row["name"], resp.StatusCode)
		}
	}
	row, err := st.GetSecret("REGION")
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsEncrypted(row.Value) {
		t.Fatalf("the variable is stored in the clear: %q", row.Value)
	}
	resp, err := http.Get(srv.URL + "/api/v1/secrets")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"value":"us-west-2"`) || strings.Contains(string(body), "supersecret") {
		t.Errorf("list = %s, want the variable's plaintext and no secret value", body)
	}
}
