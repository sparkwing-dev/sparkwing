package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRegisteredRunnerZeroHeadroomRefusesAward(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		registeredRunnerZeroHeadroom(t, `{"resources":{"set":true,"value":{"cores":{"set":true,"value":4}}}}`, 4)
	})
	t.Run("implicit", func(t *testing.T) { registeredRunnerZeroHeadroom(t, `{}`, 1) })
}

func registeredRunnerZeroHeadroom(t *testing.T, execution string, available float64) {
	t.Helper()
	server, session := credentialSessionServer(t)
	e := &EngineController{Store: server.store, RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })}
	engineSQL(t, e.Store, `UPDATE memberships SET role='owner' WHERE account_id='member'`)
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := e.Store.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, RegistrationID: "runner:a:capacity-machine", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "capacity-machine"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"example.test/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	bearer := issued.Raw
	handler := server.WithEngineController(e).Handler()
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
	engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE run_id='run' AND node_id='job'`, []byte(execution))
	request := `{"runner_version":"v1.2.3","kinds":["node"],"capacity":{"cores":8,"memory_bytes":8192},"available_cores":0,"available_memory_bytes":0,"os":"linux","arch":"arm64"}`
	response := engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", request, 200)
	var claim controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil || claim.Claim != nil {
		t.Fatalf("saturated registered machine received work: %s error=%v", response.Body, err)
	}
	request = fmt.Sprintf(`{"runner_version":"v1.2.3","kinds":["node"],"capacity":{"cores":8,"memory_bytes":8192},"available_cores":%g,"available_memory_bytes":4096,"os":"linux","arch":"arm64"}`, available)
	response = engineClaimHTTP(t, handler, bearer, "POST", "/api/v1/runners/claim", request, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil || claim.Claim == nil || claim.Claim.JobID != "job" || claim.Claim.Runner.Name != "capacity-machine" {
		t.Fatalf("available registered machine lost its award: %s error=%v", response.Body, err)
	}
}
