package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestBurstCloudClaimsRespectUserOverflowAndLeavePersonalCapacityAvailable(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			e, tenant := engineRuntimeStore(t)
			engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_fingerprint='fp' WHERE name='a'`)
			engineSQL(t, e.Store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('a','funded-a','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
			budget := int64(1)
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			run.Request.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "machines"}, {Target: "cloud"}}, OverflowMinutes: &budget}
			run.Request.Request.Git.Repository.Display = "https://github.com/acme/repo"
			request := sealWith(t, run.Request, run.Request.Provenance)
			engineSQL(t, e.Store, `UPDATE runs SET request_json=?,created_principal='account',created_subject_kind='account'`, request)
			for _, id := range []string{"one", "two"} {
				engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), []byte(`{"requires":{"set":true,"value":["sparkwing-4cpu,team-4cpu"]}}`))
			}
			engineAcceptedFixturePlan(t, e, "a", "run", "one", "two")
			e.claimProfiles = billingCloudProfile{}
			e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
			cloud := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
			h := New(e.Store, nil).WithEngineController(e.EngineController).Handler()
			engineClaimHTTP(t, h, cloud, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"one"}`, http.StatusOK)
			body := `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"two"}`
			status := http.StatusConflict
			if queued {
				body = `{"runner_version":"v1","kinds":["node"]}`
				status = http.StatusOK
			}
			response := engineClaimHTTP(t, h, cloud, "POST", "/api/v1/launcher/claim", body, status)
			if queued {
				var poll controllerwire.ClaimResponse
				if err := json.Unmarshal(response.Body.Bytes(), &poll); err != nil {
					t.Fatal(err)
				}
				if poll.Claim != nil {
					t.Fatal("queued Cloud bypassed overflow limit")
				}
			}
			engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','account@example.test',1,1)`)
			engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','editor',1)`)
			engineSQL(t, e.Store, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('personal-session','account','runs.read,runs.write',1,?,'a','account')`, time.Now().Add(time.Hour).UnixNano())
			engineSQL(t, e.Store, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('personal-session','a',0)`)
			authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "personal-session"}
			machine, err := e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &authority, RegistrationID: "runner:a:own-machine", Repositories: []string{"github.com/acme/*"}, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "own-machine"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
			if err != nil {
				t.Fatal(err)
			}
			local := machine.Raw
			engineClaimHTTP(t, h, local, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"two","labels":["team-4cpu"]}`, http.StatusOK)
			node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "two"})
			if err != nil || node.ClaimedBy != "own-machine" {
				t.Fatalf("local continuation: %+v %v", node, err)
			}
		})
	}
}

func TestBurstOverflowAPIUsesAuthenticatedAccount(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','a@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','editor',1)`)
	issued, _, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, nil).Handler()
	call := func(method, body string, status int) *overflowResponse {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/overflow", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+issued)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
		if status != http.StatusOK {
			return nil
		}
		var result overflowResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return &result
	}
	if result := call("GET", "", http.StatusOK); result.MonthlyMinutes != nil {
		t.Fatal("default user budget is capped")
	}
	if result := call("PUT", `{"monthly_minutes":12}`, http.StatusOK); result.MonthlyMinutes == nil || *result.MonthlyMinutes != 12 {
		t.Fatalf("set budget: %+v", result)
	}
	if result := call("PUT", `{"monthly_minutes":null}`, http.StatusOK); result.MonthlyMinutes != nil {
		t.Fatal("budget clear failed")
	}
	call("PUT", `{"monthly_minutes":-1}`, http.StatusBadRequest)
	writeOnly, _, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, writeOnly, "PUT", "/api/v1/overflow", `{"monthly_minutes":5}`, http.StatusOK)
	engineClaimHTTP(t, h, writeOnly, "GET", "/api/v1/overflow", "", http.StatusForbidden)
}

type overflowResponse struct {
	MonthlyMinutes *int64 `json:"monthly_minutes"`
}

func TestBurstOverflowClientControlsAuthenticatedAccount(t *testing.T) {
	s, options, _, _ := sourceUploadFixture(t)
	options.HTTP = &http.Client{Transport: inventoryRoundTrip{s.Handler()}}
	manager, err := client.NewOverflowManager(options)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := manager.ReadOverflowControl(t.Context())
	if err != nil || initial.MonthlyMinutes != nil || initial.UsedSeconds != 0 || initial.PeriodStart.Day() != 1 {
		t.Fatalf("default control: %+v %v", initial, err)
	}
	minutes := int64(12)
	changed, err := manager.SetOverflowLimit(t.Context(), &minutes)
	if err != nil || changed.MonthlyMinutes == nil || *changed.MonthlyMinutes != minutes {
		t.Fatalf("set control: %+v %v", changed, err)
	}
	persisted, err := manager.ReadOverflowControl(t.Context())
	if err != nil || persisted.MonthlyMinutes == nil || *persisted.MonthlyMinutes != minutes {
		t.Fatalf("persisted control: %+v %v", persisted, err)
	}
	cleared, err := manager.SetOverflowLimit(t.Context(), nil)
	if err != nil || cleared.MonthlyMinutes != nil {
		t.Fatalf("clear control: %+v %v", cleared, err)
	}
}

func TestBurstPersonalEnrollmentAndClaimsUseTheAccountOwner(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e, tenant := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','a@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','editor',1)`)
	server := e.Server.WithEngineController(e.EngineController)
	raw, _, _, err := e.Store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "account"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	e.claimProfiles = billingCloudProfile{}
	h := server.Handler()
	digest := sha256.Sum256([]byte(raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	member, err := e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := client.NewRunnerTokenManager(client.EngineOptions{URL: "https://controller.test", Bearer: func(context.Context) (string, error) { return member.Raw, nil }, HTTP: &http.Client{Transport: localHostRoundTripper{handler: h}}})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := manager.EnrollRunner(t.Context(), controllerwire.EnrollRunnerRequest{Name: "cli-desktop", Repos: []string{"github.com/acme/*"}, Personal: true})
	if err == nil || cli != nil {
		t.Fatalf("member bearer established independent personal enrollment: %+v %v", cli, err)
	}
	if _, err := manager.EnrollRunner(t.Context(), controllerwire.EnrollRunnerRequest{Name: "team-machine", Repos: []string{"github.com/acme/*"}}); err == nil {
		t.Fatal("editor enrolled team machine")
	}

	manager, err = client.NewSessionRunnerTokenManager(t.Context(), client.EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: localHostRoundTripper{handler: h}}}, raw)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := manager.EnrollRunner(t.Context(), controllerwire.EnrollRunnerRequest{Name: "desktop", Repos: []string{"github.com/acme/*"}, Personal: true})
	if err != nil {
		t.Fatalf("personal enrollment: %v", err)
	}
	engineSQL(t, e.Store, `UPDATE teams SET members_can_add_machines=0 WHERE name='a'`)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Git.Repository.Display = "https://github.com/acme/repo"
	run.Request.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "machines"}, {Target: "cloud"}}}
	request := sealWith(t, run.Request, run.Request.Provenance)
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,repo_display='https://github.com/acme/repo',created_principal='someone-else',created_subject_kind='account'`, request)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	engineAcceptedFixturePlan(t, e, "a", "run")
	body := `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job"}`
	engineClaimHTTP(t, h, enrolled.Capability, "POST", "/api/v1/runners/claim", body, http.StatusUnauthorized)
	engineSQL(t, e.Store, `UPDATE teams SET members_can_add_machines=1 WHERE name='a'`)
	engineClaimHTTP(t, h, enrolled.Capability, "POST", "/api/v1/runners/claim", body, http.StatusForbidden)
	engineSQL(t, e.Store, `UPDATE runs SET created_principal='account'`)
	engineClaimHTTP(t, h, enrolled.Capability, "POST", "/api/v1/runners/claim", body, http.StatusOK)
	if _, err := manager.RevokeToken(t.Context(), enrolled.Prefix); err != nil {
		t.Fatalf("session revocation of enrolled machine: %v", err)
	}
	engineClaimHTTP(t, h, enrolled.Capability, "POST", "/api/v1/runners/claim", body, http.StatusUnauthorized)
}
