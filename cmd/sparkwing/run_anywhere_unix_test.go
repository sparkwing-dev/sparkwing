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
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var runAnywhereKinds = []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode}

func runAnywherePoll(t *testing.T, f cloudDispatch, raw string) *controllerwire.ClaimedUnit {
	t.Helper()
	response, err := f.open(raw, false).Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: runAnywhereKinds, OS: "linux", Arch: "amd64", AvailableCores: 2, AvailableMemoryBytes: 4 << 30})
	if err != nil {
		t.Fatal(err)
	}
	return response.Claim
}

func runAnywherePlan(t *testing.T, f cloudDispatch, created controllerwire.CreateRunResponse, bearer string) {
	t.Helper()
	engineID := engine.Identity{Version: "v1.2.3", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3", InputsDigest: strings.Repeat("c", 64)}, ManifestDigest: strings.Repeat("b", 64)}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "verify", Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: created.Request, Declarations: declarations, Plan: doc, Context: settings.Context{}})
	if err != nil {
		t.Fatal(err)
	}
	resolution := runspec.Resolved{Entrypoint: "Deploy", Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}, Engine: engineID, Env: created.Request.Request.Env}
	if _, err := f.open(bearer, false).AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: resolution}); err != nil {
		t.Fatalf("plan acceptance: %v", err)
	}
}

func runAnywhereComplete(t *testing.T, f cloudDispatch, unit *controllerwire.ClaimedUnit) {
	t.Helper()
	work := f.open(unit.Capability, false)
	started, err := work.TransitionJob(t.Context(), unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: &unit.Binding.Attempt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.TransitionJob(t.Context(), unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: &started.Attempt, Result: &controllerwire.JobResult{Outcome: "success"}}); err != nil {
		t.Fatal(err)
	}
}

func TestCloudUnselectedTriggerRunsOnALiveSelfHostedRunnerBeforeCloud(t *testing.T) {
	f := cloudDispatchTrigger(t)
	builder := cloudDispatchMachine(t, f, "builder")
	if runAnywherePoll(t, f, builder) != nil {
		t.Fatal("runner found work before the run existed")
	}
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Request.Request.Dispatch != runspec.DispatchController {
		t.Fatalf("unselected Cloud trigger sealed dispatch=%s", created.Request.Request.Dispatch)
	}
	if _, err := f.state.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "run-anywhere-test", time.Hour); err != nil {
		t.Fatal(err)
	}
	cloud := newCloudDispatchLauncher(t, f)
	if launched, err := cloud.runtime.LaunchOne(t.Context()); err != nil || launched {
		t.Fatalf("launcher took the plan while a better-ranked runner was live: %v %v", launched, err)
	}
	plan := runAnywherePoll(t, f, builder)
	if plan == nil || plan.Unit != capability.UnitPlan || plan.Delivery != capability.DeliveryDirect || plan.RunID != created.RunID {
		t.Fatalf("runner plan award: %+v", plan)
	}
	runAnywherePlan(t, f, created, plan.Capability)
	if launched, err := cloud.runtime.LaunchOne(t.Context()); err != nil || launched {
		t.Fatalf("launcher took the job while a better-ranked runner was live: %v %v", launched, err)
	}
	job := runAnywherePoll(t, f, builder)
	if job == nil || job.Unit != capability.UnitNode || job.JobID != "verify" {
		t.Fatalf("runner job award: %+v", job)
	}
	runAnywhereComplete(t, f, job)
	detail, err := f.member.ReadRun(t.Context(), created.RunID)
	if err != nil || detail.Status != string(store.RunSuccess) {
		t.Fatalf("run-anywhere outcome: %+v %v", detail, err)
	}
	var charges, metered int
	if err := f.state.DB().QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM credit_charges),(SELECT COUNT(*) FROM billing_awards WHERE metered=1)`).Scan(&charges, &metered); err != nil || charges != 0 || metered != 0 {
		t.Fatalf("self-hosted run-anywhere work was billed: charges=%d metered=%d %v", charges, metered, err)
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
	runAnywherePoll(t, f, builder)
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
	if _, err := f.state.DB().ExecContext(t.Context(), `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('ranking-admin','admin@example.test',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.DB().ExecContext(t.Context(), `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','ranking-admin','owner',1)`); err != nil {
		t.Fatal(err)
	}
	session, _, _, err := f.state.CreateAccountSession(t.Context(), store.Account{ID: "ranking-admin"}, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := f.state.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "default", Authority: &authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "ranking-admin"}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.TeamAdmin}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Raw
}

func TestRunAnywhereJobWaitsForTheRunnerHeadroomItsPlanDidNotNeed(t *testing.T) {
	f := cloudDispatchTrigger(t)
	builder := cloudDispatchMachine(t, f, "builder")
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "run-anywhere-test", time.Hour); err != nil {
		t.Fatal(err)
	}
	poll := func(cores float64) *controllerwire.ClaimedUnit {
		response, err := f.open(builder, false).Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: runAnywhereKinds, OS: "linux", Arch: "amd64", AvailableCores: cores, AvailableMemoryBytes: 4 << 30})
		if err != nil {
			t.Fatal(err)
		}
		return response.Claim
	}
	plan := poll(0.25)
	if plan == nil || plan.Unit != capability.UnitPlan {
		t.Fatalf("a busy runner did not take the plan: %+v", plan)
	}
	runAnywherePlan(t, f, created, plan.Capability)
	for range 3 {
		if job := poll(0.25); job != nil {
			t.Fatalf("a job needing one core went to a runner offering a quarter: %+v", job)
		}
	}
	var asks int
	if err := f.state.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM unit_asks`).Scan(&asks); err != nil || asks != 0 {
		t.Fatalf("a claimant that does not fit recorded %d asks: %v", asks, err)
	}
	if job := poll(2); job == nil || job.Unit != capability.UnitNode || job.JobID != "verify" {
		t.Fatalf("the job did not go to the runner once it had headroom: %+v", job)
	}
}
