package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRegisteredRunnerRequiresEveryRequestedLabel(t *testing.T) {
	original, session := credentialSessionServer(t)
	connection, err := original.store.Connection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fixture := &serverFixture{}
	st, err := teststore.OpenExisting(strings.TrimPrefix(connection.Target, "sqlite:"), fixture.option(original.deployment, nil))
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
	issued, err := e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, RegistrationID: "runner:a:capacity-machine", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "capacity-machine"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"example.test/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	bearer := issued.Raw
	handler := server.WithEngineController(e.EngineController).Handler()
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	var requestBody []byte
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&requestBody); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(requestBody, &sealed); err != nil {
		t.Fatal(err)
	}
	sealed.Request.Git.Repository.Display = "https://example.test/acme/repo"
	requestBody, err = json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,repo_display=? WHERE id='run'`, requestBody, sealed.Request.Git.Repository.Display)
	engineAcceptedFixturePlan(t, e, "a", "run")
	engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE run_id='run' AND node_id='job'`, []byte(`{"requires":{"set":true,"value":["team-4cpu","tool:terraform"]}}`))
	request := `{"runner_version":"v1.2.3","kinds":["node"],"labels":["team-4cpu"],"os":"linux","arch":"arm64"}`
	response := engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", request, 200)
	var claim controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil || claim.Claim != nil {
		t.Fatalf("machine missing a required label received work: %s error=%v", response.Body, err)
	}
	request = `{"runner_version":"v1.2.3","kinds":["node"],"labels":["team-4cpu","tool:terraform"],"os":"linux","arch":"arm64"}`
	response = engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", request, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil || claim.Claim == nil || claim.Claim.JobID != "job" || claim.Claim.Runner.Name != "capacity-machine" {
		t.Fatalf("matching registered machine lost its award: %s error=%v", response.Body, err)
	}
}
