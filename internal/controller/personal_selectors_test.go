package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPersonalRunnerClaimsControllerPlanRunSelector(t *testing.T) {
	for _, selector := range []string{"os=darwin", "host=moonborn"} {
		for _, selection := range []string{"runner", "requires"} {
			t.Run(selector+"/"+selection, func(t *testing.T) {
				e, tenant := engineRuntimeStore(t)
				server := &serverFixture{Server: e.Server, store: e.Store, engine: e}
				e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
				run, err := tenant.ReadRun(t.Context(), "run")
				if err != nil {
					t.Fatal(err)
				}
				if selection == "runner" {
					run.Request.Request.Options.Runner.Optional = runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerLabel, Value: selector}}
				} else {
					run.Request.Request.Options.Requires.Optional = runspec.Optional[[]string]{Set: true, Value: []string{selector}}
					labels, err := json.Marshal([]string{selector})
					if err != nil {
						t.Fatal(err)
					}
					engineSQL(t, e.Store, `UPDATE triggers SET needs_labels=?`, labels)
				}
				run.Request.Request.Dispatch = runspec.DispatchController
				provenance, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "account"), run.Request.Provenance.Ref(), run.Request.Provenance.SHA(), run.Pipeline)
				if err != nil {
					t.Fatal(err)
				}
				run.Request = withProvenance(run.Request, provenance)
				body, err := json.Marshal(run.Request)
				if err != nil {
					t.Fatal(err)
				}
				engineSQL(t, e.Store, `UPDATE runs SET request_json=?,dispatch='controller'`, body)
				engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
				engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','account@example.test',1,1)`)
				engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','editor',1)`)
				engineSQL(t, e.Store, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('personal-session','account','runs.read,runs.write',1,?,'a','account')`, time.Now().Add(time.Hour).UnixNano())
				engineSQL(t, e.Store, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('personal-session','a',0)`)
				authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "personal-session"}
				machine, err := e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &authority, RegistrationID: "runner:a:moonborn", AllowedLabels: []string{"host=moonborn", "os=darwin", "arch=arm64"}, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "moonborn"}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
				if err != nil {
					t.Fatal(err)
				}
				if err := e.Store.AsOperator().RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: machine.Capability.Prefix, Labels: []string{"host=moonborn", "os=darwin", "arch=arm64"}, OS: "darwin", Arch: "arm64"}); err != nil {
					t.Fatal(err)
				}
				h := server.WithEngineController(e.EngineController).Handler()
				response := engineClaimHTTP(t, h, machine.Raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["plan"],"labels":["host=moonborn","os=darwin","arch=arm64"],"os":"darwin","arch":"arm64"}`, 200)
				var planClaim controllerwire.ClaimResponse
				if err := json.Unmarshal(response.Body.Bytes(), &planClaim); err != nil || planClaim.Claim == nil {
					t.Fatalf("matching personal planner left plan pending: %s %v", response.Body, err)
				}
				owner, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(planClaim.Claim.Capability)))
				if err != nil {
					t.Fatal(err)
				}
				acceptCapacityDocument(t, server, tenant, owner, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "where", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
				other, err := e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &authority, RegistrationID: "runner:a:desktop", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "desktop"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
				if err != nil {
					t.Fatal(err)
				}
				server.WithIdleClaimPollEnforced(true)
				server.recordQueueActivity(time.Now().Add(-testIdleWindow))
				empty := engineClaimHTTP(t, h, other.Raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"os":"linux","arch":"amd64"}`, 200)
				var unmatched controllerwire.ClaimResponse
				if err := json.Unmarshal(empty.Body.Bytes(), &unmatched); err != nil || unmatched.Claim != nil {
					t.Fatalf("unmatched runner received selected job: %s %v", empty.Body, err)
				}
				response = engineClaimHTTP(t, h, machine.Raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"labels":["host=moonborn","os=darwin","arch=arm64"],"os":"darwin","arch":"arm64"}`, 200)
				var claim controllerwire.ClaimResponse
				if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil || claim.Claim == nil || claim.Claim.JobID != "where" {
					t.Fatalf("matching personal runner left job pending: %s %v", response.Body, err)
				}
			})
		}
	}
}
