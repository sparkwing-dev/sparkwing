package acceptance_test

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestGitHubEnrollmentHTTPUsesDefaultTransactions(t *testing.T) {
	f := newDeploymentSessionFlow(t, deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: "https://controller.test"}, license.FeatureMultiTeam))
	if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE memberships SET role='owner' WHERE team='team' AND account_id='member'`); err != nil {
		t.Fatal(err)
	}
	actions := newActionsExchangeFixture(t)
	actions.server = f.server.WithGitHubRunners(githuboidc.Config{
		Audience: "https://controller.test", Issuer: "https://actions.test", JWKSURL: "https://actions.test/keys",
		HTTP: &http.Client{Transport: reportTransport{http.HandlerFunc(actions.signer.ServeKeys)}},
	})
	f.store.SetProvisionPolicy(actionsProvisionPolicy(func(capability.ProvisionSpec) error { return nil }))
	call := func(method, path, body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, f.request(method, path, body))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	call("POST", "/api/v1/runners/github?team=foreign", `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7}`, 201)
	call("POST", "/api/v1/runners/github", `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7}`, 409)
	call("GET", "/api/v1/runners/github", "", 200)
	tenant, err := f.store.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GitHubRunnerBinding(t.Context(), 42)
	if err != nil || row.CreatedBy != "member" {
		t.Fatalf("persisted enrollment: %+v %v", row, err)
	}
	var storedAt int64
	if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT created_at FROM github_runner_bindings WHERE team='team' AND repository_id=42`).Scan(&storedAt); err != nil || storedAt != row.CreatedAt.UnixNano() {
		t.Fatalf("enrollment timestamp: %d %v", storedAt, err)
	}
	w := actions.request(t, "POST", actions.token(t), "team", nil)
	var issued struct{ Token string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &issued) != nil || issued.Token == "" {
		t.Fatalf("exchange: %d %s", w.Code, w.Body.String())
	}
	root, err := f.store.LookupCapability(t.Context(), sha256.Sum256([]byte(issued.Token)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.store.DelegateCapability(t.Context(), capability.DelegateSpec{
		Parent: root.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim},
		Binding: root.Capability.Binding, Repositories: root.Capability.Repositories, ExpiresAt: new(time.Now().Add(time.Minute)),
	})
	if err != nil {
		t.Fatal(err)
	}
	call("DELETE", "/api/v1/runners/github/42?team=foreign", "", 204)
	for _, digest := range [][32]byte{root.Capability.Digest, child.Capability.Digest} {
		if _, err := f.store.LookupCapability(t.Context(), digest); err == nil {
			t.Fatal("HTTP revocation left root or descendant usable")
		}
	}
	w = actions.request(t, "POST", actions.token(t), "team", nil)
	if w.Code != 403 {
		t.Fatalf("removed enrollment exchanged: %d %s", w.Code, w.Body.String())
	}
	if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE memberships SET role='reader' WHERE team='team' AND account_id='member'`); err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/v1/runners/github", `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7}`, 403)
}
