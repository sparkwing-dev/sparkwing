package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type tenantRunnerPolicy struct{}

func (tenantRunnerPolicy) CheckRunner(context.Context, string) error { return nil }

func tenantRunClaim(t *testing.T, f *tenancyFixture) (controllerwire.CreateRunResponse, controllerwire.ClaimedUnit) {
	t.Helper()
	f.srv.WithEngineController(&controller.EngineController{Store: f.st, RunnerPolicy: tenantRunnerPolicy{}})
	body := computeIntake("build-b")
	body.Request.Git.Repository = runspec.Repository{}
	body.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://git.example.test/acme/widgets.git", PipelineDir: ".sparkwing"}
	body.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "b-runner"}}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "runner"}}
	if code, response := f.do("POST", "/api/v1/runs", f.runnerB, body); code != http.StatusForbidden {
		t.Fatalf("runner admitted a run = %d: %s", code, response)
	}
	code, response := f.do("POST", "/api/v1/runs", f.ownerB, body)
	var admitted controllerwire.CreateRunResponse
	if code != http.StatusCreated || json.Unmarshal([]byte(response), &admitted) != nil || admitted.RunID == "" {
		t.Fatalf("member intake = %d: %s", code, response)
	}
	code, response = f.do("POST", "/api/v1/runners/claim?team="+string(f.teamA.Team()), f.runnerB, controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitTrigger}, AvailableCores: 1})
	var claimed controllerwire.ClaimResponse
	if code != http.StatusOK || json.Unmarshal([]byte(response), &claimed) != nil || claimed.Claim == nil || claimed.Claim.RunID != admitted.RunID || claimed.Claim.Capability == "" {
		t.Fatalf("runner claim = %d: %s", code, response)
	}
	return admitted, *claimed.Claim
}

func TestTeamBoundary_ARunnerClaimsItsOwnTeamsAdmittedRun(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		admitted, claim := tenantRunClaim(t, f)
		if claim.Binding.RunID != admitted.RunID || claim.Request.Request.Team != string(f.teamB.Team()) {
			t.Fatalf("claim crossed teams: %+v", claim)
		}
		if _, err := f.teamB.ReadRun(t.Context(), admitted.RunID); err != nil {
			t.Fatal(err)
		}
		for _, other := range []*store.Tenant{f.teamA, forTeam(t, f.st, store.DefaultTeam)} {
			if _, err := other.ReadRun(t.Context(), admitted.RunID); err == nil {
				t.Fatalf("run is visible in %s", other.Team())
			}
		}
		property := func(forged string) bool {
			for _, named := range []bool{false, true} {
				body := controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitTrigger}, AvailableCores: 1}
				want := http.StatusOK
				if named {
					body.RunID, want = f.runA, http.StatusForbidden
				}
				code, response := f.do("POST", "/api/v1/runners/claim?team="+url.QueryEscape(forged), f.runnerB, body)
				if code != want || strings.Contains(response, f.runA) || strings.Contains(response, `"capability"`) {
					t.Fatalf("foreign claim = %d: %s", code, response)
				}
				if !named {
					var empty controllerwire.ClaimResponse
					if err := json.Unmarshal([]byte(response), &empty); err != nil || empty.Claim != nil {
						t.Fatalf("empty own queue = %s: %v", response, err)
					}
				}
			}
			var awards int
			if err := f.st.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE issuance='award'`).Scan(&awards); err != nil {
				t.Fatal(err)
			}
			return awards == 1
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTeamBoundary_ARunnerOfAnotherTeamFinishesTheRunItClaimed(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		admitted, claim := tenantRunClaim(t, f)
		path := "/api/v1/runs/" + admitted.RunID
		plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}
		declarations := runspec.CheckoutDeclarations{Version: 1}
		resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
		if err != nil {
			t.Fatal(err)
		}
		rawPlan, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		body := controllerwire.PlanSubmission{Declarations: declarations, Plan: rawPlan, Resolved: runspec.Resolved{SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}
		auth := "Bearer " + claim.Capability
		for _, step := range []struct {
			suffix string
			body   any
		}{
			{"/plan", body},
			{"/heartbeat", controllerwire.RenewalRequest{}},
			{"/finish", controllerwire.FinishRequest{Outcome: "success"}},
		} {
			if code, response := f.do("POST", path+step.suffix, auth, step.body); code != http.StatusOK {
				t.Fatalf("claim %s = %d: %s", step.suffix, code, response)
			}
		}
		row, err := f.teamB.ReadRun(t.Context(), admitted.RunID)
		if err != nil || row.State != store.RunSuccess || row.Spec == nil || row.PlanHash == "" {
			t.Fatalf("run did not seal and finish: %+v: %v", row, err)
		}
	})
}
