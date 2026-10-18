package controller_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc/githuboidctest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestGitHubAppOwnerSelectionSealsDispatchAndActionsClaimsItsTrigger(t *testing.T) {
	issuer := githuboidctest.New(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithCloudDeployment(true).WithExternalURL(ghAudience).WithGitHubRunners(issuer.Config(ghAudience))
	})
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	want := map[string]runspec.Dispatch{"cloud-build": runspec.DispatchController, "actions-build": runspec.DispatchHost, "pool-build": runspec.DispatchHost}
	for pipeline, runner := range map[string]string{"cloud-build": "", "actions-build": "github-actions", "pool-build": "pool=prod"} {
		if code := f.subscribe(olga, "acme/widgets", pipeline, map[string]any{"runner": runner}); code != http.StatusOK {
			t.Fatalf("subscribe %s runner=%q: %d", pipeline, runner, code)
		}
	}
	if code := f.subscribe(olga, "acme/widgets", "bad-build", map[string]any{"runner": "laptop"}); code != http.StatusBadRequest {
		t.Fatalf("unknown owner selection = %d, want 400", code)
	}
	code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	ids := startedRunIDs(t, out)
	if code != http.StatusAccepted || len(ids) != 3 {
		t.Fatalf("push = %d %v, want three runs", code, out)
	}
	actionsRun := ""
	for _, id := range ids {
		sealed := f.sealedRequest(id)
		if sealed.Dispatch != want[sealed.Pipeline] {
			t.Fatalf("%s sealed dispatch=%s, want %s", sealed.Pipeline, sealed.Dispatch, want[sealed.Pipeline])
		}
		if sealed.Pipeline == "actions-build" {
			actionsRun = id
			if sealed.Options.Runner.Value.Kind != runspec.RunnerActions {
				t.Fatalf("Actions owner selection not sealed: %+v", sealed.Options.Runner)
			}
		}
	}
	if code := f.call("POST", "/api/v1/runners/github", olga.auth, map[string]any{"repository": "acme/widgets", "repository_id": 701, "repository_owner_id": 70}, nil); code != http.StatusCreated {
		t.Fatalf("bind Actions repository = %d", code)
	}
	var credential ghCredential
	job := githuboidctest.Job{Repository: "acme/widgets", RepositoryID: 701, RepositoryOwnerID: 70, Ref: "refs/heads/main", SHA: headSHA, RunID: "900"}
	if code := f.call("POST", "/api/v1/runners/github/exchange", "", map[string]string{"id_token": issuer.Token(job, ghAudience), "team": olga.team}, &credential); code != http.StatusCreated {
		t.Fatalf("Actions exchange = %d", code)
	}
	tenant, err := f.store.ForTeam(t.Context(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	eligible := func(run string, kind capability.UnitKind) bool {
		t.Helper()
		ok, err := tenant.UnitHasEligibleClaimant(t.Context(), run, store.UnclaimedUnit{Kind: kind}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if eligible(actionsRun, capability.UnitTrigger) {
		t.Fatal("an Actions runner that never polled counts as eligible")
	}
	var empty controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", "Bearer "+credential.Token, githubClaimBody(capability.UnitNode, "", ""), &empty); code != http.StatusOK || empty.Claim != nil {
		t.Fatalf("Actions node poll = %d %+v", code, empty.Claim)
	}
	if !eligible(actionsRun, capability.UnitTrigger) {
		t.Fatal("a polling Actions runner bound to the push is not eligible for its trigger")
	}
	for _, id := range ids {
		if sealed := f.sealedRequest(id); sealed.Pipeline == "cloud-build" && eligible(id, capability.UnitPlan) {
			t.Fatal("an Actions runner counts as eligible for a launcher plan")
		}
	}
	var claimed controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", "Bearer "+credential.Token, githubClaimBody(capability.UnitTrigger, "", ""), &claimed); code != http.StatusOK || claimed.Claim == nil || claimed.Claim.RunID != actionsRun || claimed.Claim.Unit != capability.UnitTrigger {
		t.Fatalf("Actions trigger claim = %d %+v, want %s", code, claimed.Claim, actionsRun)
	}
}

func TestGitHubAppAutomationOwnerSelectionReachesPushesAndArmedSchedules(t *testing.T) {
	f := newAppFixture(t, func(s *controller.Server) *controller.Server { return s.WithCloudDeployment(true) })
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig+`  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        - cron: "0 3 * * *"
          where: controller
`))
	var preview map[string]any
	if code := f.call("PUT", "/api/v1/team/github-app/automation", olga.auth, map[string]string{"repository": "acme/widgets", "runner": "github-actions"}, &preview); code != http.StatusOK || preview["runner"] != "github-actions" {
		t.Fatalf("enable with Actions owner = %d %v", code, preview)
	}
	if code := f.call("PUT", "/api/v1/team/github-app/automation", olga.auth, map[string]string{"repository": "acme/widgets", "runner": "pool="}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty pool selector = %d, want 400", code)
	}
	code, out := f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")
	ids := startedRunIDs(t, out)
	if code != http.StatusAccepted || len(ids) != 1 {
		t.Fatalf("automation push = %d %v", code, out)
	}
	if sealed := f.sealedRequest(ids[0]); sealed.Dispatch != runspec.DispatchHost || sealed.Options.Runner.Value.Kind != runspec.RunnerActions {
		t.Fatalf("automation push sealed dispatch=%s runner=%+v", sealed.Dispatch, sealed.Options.Runner)
	}
	rows := appCronRows(t, f, olga.team)
	if len(rows) != 1 {
		t.Fatalf("armed schedules = %+v", rows)
	}
	for runner, want := range map[string]runspec.Dispatch{"github-actions": runspec.DispatchHost, "cloud": runspec.DispatchController} {
		if code := f.call("PUT", "/api/v1/team/github-app/automation", olga.auth, map[string]string{"repository": "acme/widgets", "runner": runner}, nil); code != http.StatusOK {
			t.Fatalf("set automation runner %s = %d", runner, code)
		}
		var started controllerwire.ScheduleRun
		if code := f.call("POST", "/api/v1/schedules/"+rows[0].ID+"/run", olga.auth, nil, &started); code != http.StatusOK || started.RunID == "" {
			t.Fatalf("run App schedule = %d %+v", code, started)
		}
		if sealed := f.sealedRequest(started.RunID); sealed.Dispatch != want {
			t.Fatalf("App schedule with runner %s sealed dispatch=%s, want %s", runner, sealed.Dispatch, want)
		}
	}
}
