package acceptance_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialTransactionsHTTPUseDefaultStoreAdapters(t *testing.T) {
	f := newSessionFlow(t)
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE memberships SET role='owner' WHERE team='team' AND account_id='member'`); err != nil {
		t.Fatal(err)
	}
	h := f.server.Handler()
	call := func(method, path, body string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request(method, path, body))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	call("POST", "/api/v1/secrets?team=foreign", `{"name":"KEY","value":"private-value","pipeline":"deploy","masked":false}`, 204)
	body := call("GET", "/api/v1/secrets/KEY?pipeline=deploy&team=foreign", "", 200)
	var row struct{ Value, Principal, Pipeline string }
	if err := json.Unmarshal([]byte(body), &row); err != nil || row.Value != "private-value" || row.Principal != "member" || row.Pipeline != "deploy" {
		t.Fatalf("secret response: %s %v", body, err)
	}
	call("POST", "/api/v1/secrets", `{"name":"MASKED","value":"private-masked"}`, 204)
	if body := call("GET", "/api/v1/secrets/MASKED", "", 403); strings.Contains(body, "private-masked") {
		t.Fatal("masked value escaped member read")
	}
	call("POST", "/api/v1/team/git-credentials?team=foreign", `{"host":"git.example.test","kind":"https","token":"private-key"}`, 200)
	tenant, err := f.store.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := tenant.GitCredentialForHost(t.Context(), "git.example.test")
	if err != nil || credential.Team != "team" || credential.CreatedBy != "member" || credential.ConfirmedAt == nil || credential.Secret == "private-key" {
		t.Fatalf("committed git credential: %+v %v", credential, err)
	}
	foreign, err := f.store.ForTeam(t.Context(), "foreign")
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := foreign.GitCredentials(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("foreign credentials changed: %d %v", len(rows), err)
	}
	if _, err := foreign.GetSecretRow("KEY", "deploy"); err == nil {
		t.Fatal("foreign secret changed")
	}
	call("DELETE", "/api/v1/team/git-credentials/git.example.test", "", 204)
	call("DELETE", "/api/v1/secrets/KEY?pipeline=deploy", "", 204)
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE memberships SET role='reader' WHERE team='team' AND account_id='member'`); err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/v1/secrets", `{"name":"DENIED","value":"private-value"}`, 403)
	call("POST", "/api/v1/team/git-credentials", `{"host":"git.example.test","kind":"https","token":"private-key"}`, 403)
	if _, err := tenant.GetSecretRow("DENIED", ""); err == nil {
		t.Fatal("demoted member stored secret")
	}
	if _, err := tenant.GitCredentialForHost(t.Context(), "git.example.test"); err == nil {
		t.Fatal("demoted member stored git credential")
	}
	if strings.Contains(f.logs.String(), "private-") {
		t.Fatal("credential values entered request audits")
	}
}
