//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

var runAnywhereKinds = []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode}

func runAnywherePoll(t *testing.T, f cloudDispatch, raw string, freeSlots int) *controllerwire.ClaimedUnit {
	t.Helper()
	response, err := f.open(raw, false).Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: runAnywhereKinds, OS: "linux", Arch: "amd64", FreeSlots: &freeSlots})
	if err != nil {
		t.Fatal(err)
	}
	return response.Claim
}

func runAnywherePlan(t *testing.T, f cloudDispatch, created controllerwire.CreateRunResponse, bearer string) {
	t.Helper()
	engineID := engineid.Identity{Version: "v1.2.3", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3", InputsDigest: strings.Repeat("c", 64)}, ManifestDigest: strings.Repeat("b", 64)}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "verify", Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: cloudDispatchRequest(t, f, created.RunID), Declarations: declarations, Plan: doc, Context: settings.Context{}})
	if err != nil {
		t.Fatal(err)
	}
	resolution := runspec.Resolved{Entrypoint: "Deploy", Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}, Engine: engineID, Env: cloudDispatchRequest(t, f, created.RunID).Request.Env}
	if _, err := f.open(bearer, false).AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: resolution}); err != nil {
		t.Fatalf("plan acceptance: %v", err)
	}
}

func TestCloudUnselectedUploadSkipsALiveRepositoryScopedRunner(t *testing.T) {
	f := cloudDispatchTrigger(t)
	builder := cloudDispatchMachine(t, f, "builder")
	if runAnywherePoll(t, f, builder, 1) != nil {
		t.Fatal("runner found work before the run existed")
	}
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	request := cloudDispatchRequest(t, f, created.RunID)
	if request.Request.Dispatch != runspec.DispatchController || !request.Provenance.Valid() || request.Provenance.ProvenRepository() != "" {
		t.Fatalf("upload intake: dispatch=%s provenance=%+v", request.Request.Dispatch, request.Provenance)
	}
	if plan := runAnywherePoll(t, f, builder, 2); plan != nil {
		t.Fatalf("repository-scoped runner claimed an unproven upload: %+v", plan)
	}
	cloud := newCloudDispatchLauncher(t, f)
	plan := cloud.launch(t, capability.UnitPlan, "")
	if plan.RunID != created.RunID {
		t.Fatalf("launcher planned %s, want %s", plan.RunID, created.RunID)
	}
	runAnywherePlan(t, f, created, plan.Capability)
	cloud.finish(t)
	if job := runAnywherePoll(t, f, builder, 1); job != nil {
		t.Fatalf("repository-scoped runner claimed an unproven job: %+v", job)
	}
	cloud.complete(t, cloud.launch(t, capability.UnitNode, "verify"))
	detail, err := f.member.ReadRun(t.Context(), created.RunID)
	if err != nil || detail.Status != string(runstate.RunSuccess) {
		t.Fatalf("run-anywhere outcome: %+v %v", detail, err)
	}
}

func TestTeamAdminRanksCloudFirstFromTheCLI(t *testing.T) {
	f := cloudDispatchTrigger(t)
	admin := runAnywhereAdmin(t, f)
	config, err := os.ReadFile(os.Getenv("SPARKWING_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	config = []byte(strings.Replace(string(config), "profiles:\n", "profiles:\n  admin: { controller: { url: "+f.endpoint+", token: "+admin+" } }\n", 1))
	if err := os.WriteFile(os.Getenv("SPARKWING_CONFIG"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"team", "settings", "set", "--profile=cloud", "--claim-ranking=cloud,self-hosted"},
		{"team", "settings", "set", "--profile=admin", "--claim-ranking=cloud,nowhere"},
	} {
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		if err := runTeamSettings(t.Context(), inv); err == nil {
			t.Fatalf("%v changed the ranking", args)
		}
	}
	inv, err := clisurface.Parse("sparkwing", []string{"team", "settings", "set", "--profile=admin", "--claim-ranking=cloud,self-hosted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runTeamSettings(t.Context(), inv); err != nil {
		t.Fatal(err)
	}
	tenant, err := f.state.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := tenant.TeamSettings(t.Context()); err != nil || strings.Join(current.ClaimRanking, ",") != "cloud,self-hosted" {
		t.Fatalf("ranking after the CLI change: %+v %v", current, err)
	}
	builder := cloudDispatchMachine(t, f, "builder")
	runAnywherePoll(t, f, builder, 1)
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	cloud := newCloudDispatchLauncher(t, f)
	planned := cloud.launch(t, capability.UnitPlan, "")
	if planned.RunID != created.RunID {
		t.Fatalf("launcher planned %s, want %s", planned.RunID, created.RunID)
	}
}

func runAnywhereAdmin(t *testing.T, f cloudDispatch) string {
	t.Helper()
	if _, err := teststore.SQL(t, f.state).ExecContext(t.Context(), `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('ranking-admin','admin@example.test',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, f.state).ExecContext(t.Context(), `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','ranking-admin','owner',1)`); err != nil {
		t.Fatal(err)
	}
	session, _, _, err := f.state.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "ranking-admin"}, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := f.state.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: "default", Authority: &authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "ranking-admin"}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.TeamAdmin}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Raw
}

func cloudDispatchRequest(t *testing.T, f cloudDispatch, id string) runspec.SealedRequest {
	t.Helper()
	row, err := teststore.ForTeam(t, f.state, store.DefaultTeam).ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Request
}
