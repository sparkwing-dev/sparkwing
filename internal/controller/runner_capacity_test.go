package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

//sparkwing:critical
func TestRegisteredRunnerRequiresEveryRequestedLabel(t *testing.T) {
	original, session := credentialSessionServer(t)
	connection, err := original.store.Connection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fixture := &serverFixture{}
	st, err := teststore.OpenExisting(t, strings.TrimPrefix(connection.Target, "sqlite:"), fixture.option(original.deployment, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	server := fixture.Server
	e := newEngineFixture(st, EngineController{RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })})
	engineSQL(t, e.Store, `UPDATE memberships SET role='owner' WHERE account_id='member'`)
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, RegistrationID: "runner:a:capacity-machine", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "capacity-machine"}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, AllowedLabels: []string{"team-4cpu", "tool:terraform"}})
	if err != nil {
		t.Fatal(err)
	}
	bearer := issued.Raw
	handler := server.WithEngineController(e.EngineController).Handler()
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	fetchedRunFixture(t, e.Store, "a", "run", "acme/repo")
	engineAcceptedFixturePlan(t, e, "a", "run")
	engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE run_id='run' AND node_id='job'`, []byte(`{"requires":{"set":true,"value":["deploy-kikd-prod"]}}`))
	for _, kinds := range []string{`["node"]`, `["trigger"]`, `["plan"]`} {
		response := engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":`+kinds+`,"labels":["team-4cpu","deploy-kikd-prod"]}`, 403)
		for _, hint := range []string{"deploy-kikd-prod", "allowed labels", "revoke this enrollment", "sparkwing runners add --runner capacity-machine --label LABEL --force", "Self-hosted operators", "admin runner-labels"} {
			if !strings.Contains(response.Body.String(), hint) {
				t.Fatalf("missing %q: %s", hint, response.Body)
			}
		}
	}
	engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE run_id='run' AND node_id='job'`, []byte(`{"requires":{"set":true,"value":["team-4cpu","tool:terraform"]}}`))
	allowed, err := st.AsOperator().ConfigureRunnerLabels(t.Context(), "a", "capacity-machine", "member", nil)
	if err != nil || !slices.Equal(allowed, []string{"team-4cpu", "tool:terraform"}) {
		t.Fatalf("allowed labels: %v %v", allowed, err)
	}
	if _, err := st.AsOperator().ConfigureRunnerLabels(t.Context(), "a", "capacity-machine", "missing", &allowed); err == nil {
		t.Fatal("nonadministrator changed labels")
	}
	request := `{"runner_version":"v1.2.3","kinds":["node"],"labels":["team-4cpu"],"os":"linux","arch":"arm64"}`
	response := engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", request, 200)
	var claim controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil || claim.Claim != nil {
		t.Fatalf("machine missing a required label received work: %s error=%v", response.Body, err)
	}
	replacement := []string{"team-4cpu"}
	if _, err := st.AsOperator().ConfigureRunnerLabels(t.Context(), "a", "capacity-machine", "member", &replacement); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"],"labels":["team-4cpu","tool:terraform"]}`, 403)
	if _, err := st.AsOperator().ConfigureRunnerLabels(t.Context(), "a", "capacity-machine", "member", &allowed); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, st, `UPDATE capabilities SET authority_json=CAST(json_remove(CAST(authority_json AS TEXT),'$.origin.allowed_labels') AS BLOB),advertised_labels_json=? WHERE prefix=?`, []byte(`["team-4cpu","tool:terraform"]`), issued.Capability.Prefix)
	migrated, err := store.Open(strings.TrimPrefix(connection.Target, "sqlite:"), fixture.option(original.deployment, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := st.AsOperator().ConfigureRunnerLabels(t.Context(), "a", "capacity-machine", "member", nil)
	if err != nil || !slices.Equal(got, allowed) {
		t.Fatalf("migrated allowed labels: %v %v", got, err)
	}
	engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"],"labels":["deploy-kikd-prod"]}`, 403)
	empty := []string{}
	if _, err := st.AsOperator().ConfigureRunnerLabels(t.Context(), "a", "capacity-machine", "member", &empty); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(strings.TrimPrefix(connection.Target, "sqlite:"), fixture.option(original.deployment, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"],"labels":["team-4cpu"]}`, 403)
	if _, err := st.AsOperator().ConfigureRunnerLabels(t.Context(), "a", "capacity-machine", "member", &allowed); err != nil {
		t.Fatal(err)
	}
	request = `{"runner_version":"v1.2.3","kinds":["node"],"labels":["team-4cpu","tool:terraform"],"os":"linux","arch":"arm64"}`
	response = engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", request, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil || claim.Claim == nil || claim.Claim.JobID != "job" || claim.Claim.Runner.Name != "capacity-machine" {
		t.Fatalf("matching registered machine lost its award: %s error=%v", response.Body, err)
	}
}
