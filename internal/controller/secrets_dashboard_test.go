package controller_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type listedSecret struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Pipeline string `json:"pipeline"`
	Masked   bool   `json:"masked"`
	Shared   bool   `json:"shared"`
}

func listSecretsAs(t *testing.T, f *browserFixture, private ...string) map[string]listedSecret {
	t.Helper()
	code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets", "", nil)
	for _, value := range private {
		if strings.Contains(body, value) {
			t.Fatal("secret list disclosed a masked value")
		}
	}
	var response struct {
		Secrets []listedSecret `json:"secrets"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &response) != nil {
		t.Fatalf("list: HTTP %d %s", code, body)
	}
	out := make(map[string]listedSecret)
	for _, row := range response.Secrets {
		out[row.Name] = row
	}
	return out
}

func secretMember(t *testing.T, f *browserFixture, id string, role store.Role) *browserFixture {
	t.Helper()
	if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1)`, id, id+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default',?,?,1)`, id, role); err != nil {
		t.Fatal(err)
	}
	raw, csrf, _, err := f.st.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: id, Email: id + "@example.test"}, store.DefaultTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &browserFixture{t: t, st: f.st, h: f.h, sessID: raw, csrf: csrf}
}

func TestSecrets_DashboardSessionsWriteSecretsAndReadVariables(t *testing.T) {
	f := newBrowserFixture(t, nil)
	editor, reader := secretMember(t, f, "editor", store.RoleEditor), secretMember(t, f, "reader", store.RoleReader)
	for _, row := range []map[string]any{
		{"name": "DEPLOY_KEY", "value": "hunter2", "shared": true, "masked": true},
		{"name": "REGION", "value": "us-west-2", "shared": true, "masked": false},
		{"name": "WEB_TOKEN", "value": "line one\nline two", "repo": "gh:1", "pipeline": "deploy-web"},
	} {
		if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", row); code != http.StatusOK {
			t.Fatalf("write %v: HTTP %d %s", row["name"], code, body)
		}
	}
	for _, who := range []*browserFixture{f, editor, reader} {
		listed := listSecretsAs(t, who, "hunter2", "line one")
		if listed["DEPLOY_KEY"].Value != "" || listed["WEB_TOKEN"].Value != "" || !listed["DEPLOY_KEY"].Masked || !listed["WEB_TOKEN"].Masked || listed["WEB_TOKEN"].Pipeline != "deploy-web" || listed["REGION"].Value != "us-west-2" || listed["REGION"].Masked {
			t.Fatalf("secret list=%+v", listed)
		}
	}
	for _, tc := range []struct{ path, private string }{
		{"/api/v1/secrets/DEPLOY_KEY", "hunter2"},
		{"/api/v1/secrets/WEB_TOKEN?repo=gh:1&pipeline=deploy-web", "line one"},
	} {
		code, body, _ := secretsRequest(t, f, "GET", tc.path, "", nil)
		if code != http.StatusForbidden || strings.Contains(body, tc.private) || !strings.Contains(body, `"code":"forbidden"`) {
			t.Fatalf("owner read: HTTP %d %s", code, body)
		}
	}
	if code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/REGION", "", nil); code != http.StatusOK || !strings.Contains(body, "us-west-2") {
		t.Fatalf("variable read: HTTP %d %s", code, body)
	}
	for _, who := range []*browserFixture{editor, reader} {
		if code, body, _ := secretsRequest(t, who, "POST", "/api/v1/secrets", "", map[string]any{"name": "REGION", "value": "eu-west-1", "shared": true, "masked": false}); code != http.StatusForbidden {
			t.Fatalf("member write: HTTP %d %s", code, body)
		}
		if code, body, _ := secretsRequest(t, who, "DELETE", "/api/v1/secrets/DEPLOY_KEY", "", nil); code != http.StatusForbidden {
			t.Fatalf("member delete: HTTP %d %s", code, body)
		}
	}
	listed := listSecretsAs(t, f)
	if listed["REGION"].Value != "us-west-2" || listed["DEPLOY_KEY"].Name != "DEPLOY_KEY" {
		t.Fatalf("refused writes changed rows: %+v", listed)
	}
	if code, body, _ := secretsRequest(t, f, "DELETE", "/api/v1/secrets/WEB_TOKEN?repo=gh:1&pipeline=deploy-web", "", nil); code != http.StatusNoContent {
		t.Fatalf("owner delete: HTTP %d %s", code, body)
	}
	if _, exists := listSecretsAs(t, f)["WEB_TOKEN"]; exists {
		t.Fatal("deleted secret remains listed")
	}
}

func TestSecrets_ListOpensAVariablesEnvelope(t *testing.T) {
	f := newBrowserFixture(t, nil)
	for _, row := range []string{
		`{"name":"REGION","value":"us-west-2","masked":false}`,
		`{"name":"TOKEN","value":"supersecret"}`,
	} {
		w := f.serve(f.cookieRequest(http.MethodPost, "/api/v1/secrets", row))
		if w.Code != http.StatusOK {
			t.Fatalf("write secret: HTTP %d %s", w.Code, w.Body)
		}
	}
	for _, name := range []string{"REGION", "TOKEN"} {
		row, err := teststore.SecretRow(teststore.ForTeam(t, f.st, store.DefaultTeam), name, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if !secrets.IsEncrypted(row.Value) || !secrets.IsBound(row.Value) {
			t.Fatalf("%s stored without a bound envelope: %q", name, row.Value)
		}
	}
	w := f.serve(f.cookieRequest(http.MethodGet, "/api/v1/secrets", ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"value":"us-west-2"`) || strings.Contains(w.Body.String(), "supersecret") {
		t.Fatalf("list: HTTP %d %s", w.Code, w.Body)
	}
}

func secretsRequest(t *testing.T, f *browserFixture, method, path, auth string, body any) (int, string, http.Header) {
	t.Helper()
	var encoded string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		encoded = string(data)
	}
	r := f.cookieRequest(method, path, encoded)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := f.serve(r)
	return w.Code, w.Body.String(), w.Header()
}

func TestSecrets_MaskedValuesStayWriteOnlyForMembersAndDeploymentCredentials(t *testing.T) {
	f := newBrowserFixture(t, nil)
	owner := "Bearer " + f.token
	for _, row := range []map[string]any{
		{"name": "DEPLOY_KEY", "value": "hunter2", "shared": true},
		{"name": "REGION", "value": "us-west-2", "shared": true, "masked": false},
	} {
		if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", owner, row); code != http.StatusOK {
			t.Fatalf("owner bearer write: HTTP %d %s", code, body)
		}
	}
	if code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/DEPLOY_KEY", owner, nil); code != http.StatusForbidden || !strings.Contains(body, `"code":"forbidden"`) || strings.Contains(body, "hunter2") {
		t.Fatalf("owner bearer masked read: HTTP %d %s", code, body)
	}
	if code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/REGION", owner, nil); code != http.StatusOK || !strings.Contains(body, "us-west-2") {
		t.Fatalf("owner bearer variable read: HTTP %d %s", code, body)
	}
	raw, _, err := teststore.ProvisionCredential(t.Context(), f.st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "ops"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/secrets/DEPLOY_KEY", nil},
		{"GET", "/api/v1/secrets/REGION", nil},
		{"POST", "/api/v1/secrets", map[string]any{"name": "OPS_KEY", "value": "ops-value", "shared": true}},
	} {
		code, body, _ := secretsRequest(t, f, tc.method, tc.path, "Bearer "+raw, tc.body)
		if code != http.StatusForbidden || strings.Contains(body, "hunter2") || strings.Contains(body, "us-west-2") || strings.Contains(body, "ops-value") {
			t.Fatalf("deployment credential: HTTP %d %s", code, body)
		}
	}
	if _, exists := listSecretsAs(t, f)["OPS_KEY"]; exists {
		t.Fatal("deployment credential wrote a tenant secret")
	}
}

func TestSecrets_EveryResponseIsNoStore(t *testing.T) {
	f := newBrowserFixture(t, nil)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/secrets", `{"name":"DEPLOY_KEY","value":"private-key","shared":true}`, http.StatusOK},
		{"GET", "/api/v1/secrets", "", http.StatusOK},
		{"GET", "/api/v1/secrets/DEPLOY_KEY", "", http.StatusForbidden},
		{"DELETE", "/api/v1/secrets/DEPLOY_KEY", "", http.StatusNoContent},
		{"DELETE", "/api/v1/secrets/DEPLOY_KEY", "", http.StatusNotFound},
	} {
		w := f.serve(f.cookieRequest(tc.method, tc.path, tc.body))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Pragma") != "no-cache" || strings.Contains(w.Body.String(), "private-key") {
			t.Fatalf("%s %s: HTTP %d %s, cache=%q pragma=%q", tc.method, tc.path, w.Code, w.Body, w.Header().Get("Cache-Control"), w.Header().Get("Pragma"))
		}
	}
}

func TestAuthenticatedResponsesAreNoStoreAndPublicOnesAreNot(t *testing.T) {
	f := newBrowserFixture(t, nil)
	if err := teststore.SeedRun(f.st, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run", Pipeline: "ci", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNode(f.st, t.Context(), store.Node{RunID: "run", NodeID: "job", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/tokens", `{"grants":["runs.read"]}`, http.StatusCreated},
		{"GET", "/api/v1/runs/run", "", http.StatusOK},
		{"GET", "/api/v1/runs/run/jobs/job", "", http.StatusOK},
	} {
		w := f.serve(f.cookieRequest(tc.method, tc.path, tc.body))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: HTTP %d %s cache=%q", tc.method, tc.path, w.Code, w.Body, w.Header().Get("Cache-Control"))
		}
	}
	for path, want := range map[string]int{"/.well-known/jwks.json": http.StatusNotFound, "/api/v1/health": http.StatusOK} {
		w := f.serve(httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want || w.Header().Get("Cache-Control") == "no-store" {
			t.Fatalf("public GET %s: HTTP %d %s cache=%q", path, w.Code, w.Body, w.Header().Get("Cache-Control"))
		}
	}
}

func TestSecretsDashboardMaskedValuesProperty(t *testing.T) {
	property := func(value []byte, shared bool, role uint8) bool {
		owner := newBrowserFixture(t, nil)
		viewer := owner
		if role%3 == 1 {
			viewer = secretMember(t, owner, "viewer", store.RoleEditor)
		}
		if role%3 == 2 {
			viewer = secretMember(t, owner, "viewer", store.RoleReader)
		}
		private := "private-secret-" + hex.EncodeToString(value)
		repo, pipeline, path := "", "", "/api/v1/secrets/PROP_TOKEN"
		if !shared {
			repo, pipeline, path = "gh:1", "deploy", path+"?repo=gh:1&pipeline=deploy"
		}
		code, body, _ := secretsRequest(t, owner, "POST", "/api/v1/secrets", "", map[string]any{"name": "PROP_TOKEN", "value": private, "shared": shared, "repo": repo, "pipeline": pipeline})
		if code != http.StatusOK {
			t.Fatalf("write: HTTP %d %s", code, body)
		}
		listed := listSecretsAs(t, viewer, private)
		if listed["PROP_TOKEN"].Name != "PROP_TOKEN" || !listed["PROP_TOKEN"].Masked || listed["PROP_TOKEN"].Value != "" {
			return false
		}
		code, body, _ = secretsRequest(t, viewer, "GET", path, "", nil)
		return code == http.StatusForbidden && !strings.Contains(body, private) && !strings.Contains(owner.logs.String(), private)
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 24})); err != nil {
		t.Fatal(err)
	}
}
