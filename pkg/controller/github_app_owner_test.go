package controller_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc/githuboidctest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestGitHubAppOwnerSelectionSealsDispatchAndActionsClaimsItsTrigger(t *testing.T) {
	issuer := githuboidctest.New(t)
	f := newDeploymentAppFixture(t, deploymenttest.Cloud(t, ghAudience), func(s *controller.Server) *controller.Server {
		return s.WithGitHubRunners(issuer.Config(ghAudience))
	})
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	want := map[string]runspec.Dispatch{"cloud-build": runspec.DispatchController, "actions-build": runspec.DispatchHost, "pool-build": runspec.DispatchController}
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
	machine, _ := f.teamRunner(olga, "team-box")
	var queued controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitTrigger, "", ""), &queued); code != http.StatusOK || queued.Claim != nil {
		t.Fatalf("same-team machine took a queued trigger: %d", code)
	}
	if code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitTrigger, actionsRun, ""), nil); code != http.StatusForbidden {
		t.Fatalf("same-team machine named claim of the Actions-owned trigger = %d, want 403", code)
	}
	var claimed controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", "Bearer "+credential.Token, githubClaimBody(capability.UnitTrigger, "", ""), &claimed); code != http.StatusOK || claimed.Claim == nil || claimed.Claim.RunID != actionsRun || claimed.Claim.Unit != capability.UnitTrigger {
		t.Fatalf("Actions trigger claim = %d %+v, want %s", code, claimed.Claim, actionsRun)
	}
}

func TestGitHubAppAutomationOwnerSelectionReachesPushesAndArmedSchedules(t *testing.T) {
	f := newDeploymentAppFixture(t, deploymenttest.Cloud(t, "https://controller.test"))
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

// A member's manual fire of an App schedule is asserted provenance, and reserved pools
// admit only verified or verified-schedule provenance, so the pool's runner never gets it.
func TestGitHubAppPoolScheduleManualFireStaysOffItsReservedRunner(t *testing.T) {
	f := newDeploymentAppFixture(t, deploymenttest.Cloud(t, "https://controller.test"))
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	if code := f.call("PUT", "/api/v1/team/github-app/automation", olga.auth, map[string]string{"repository": "acme/widgets", "runner": "pool=prod"}, nil); code != http.StatusOK {
		t.Fatalf("enable with pool owner = %d", code)
	}
	if code, _ := f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), ""); code != http.StatusAccepted {
		t.Fatalf("arming push = %d", code)
	}
	if code := f.call("POST", "/api/v1/pools/prod/rules", olga.auth, map[string]any{"repos": []string{"acme/widgets"}}, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("pool access rule = %d", code)
	}
	var minted struct {
		Capability string `json:"capability"`
	}
	if code := f.call("POST", "/api/v1/runners", olga.auth, map[string]any{"name": "prod-box", "repos": []string{"github.com/acme/*"}, "reserved_pools": []string{"prod"}}, &minted); code != http.StatusCreated {
		t.Fatalf("enroll prod runner = %d", code)
	}
	rows := appCronRows(t, f, olga.team)
	if len(rows) != 1 {
		t.Fatalf("armed schedules = %+v", rows)
	}
	var started controllerwire.ScheduleRun
	if code := f.call("POST", "/api/v1/schedules/"+rows[0].ID+"/run", olga.auth, nil, &started); code != http.StatusOK || started.RunID == "" {
		t.Fatalf("run App schedule = %d %+v", code, started)
	}
	var claimed controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", "Bearer "+minted.Capability, githubClaimBody(capability.UnitPlan, started.RunID, ""), &claimed); code != http.StatusForbidden || claimed.Claim != nil {
		t.Fatalf("prod runner claim of a manual pool schedule fire = %d, claimed=%+v", code, claimed.Claim)
	}
}

func TestGitHubRepositoryDisplayComesFromTheAppInstallation(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	request := func(key, display string) controllerwire.CreateRunRequest {
		return controllerwire.CreateRunRequest{IdempotencyKey: key + display, Request: runspec.Request{Version: 1, Pipeline: "build", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey(key), Display: display}, SHA: headSHA}, Source: runspec.Source{Ref: headSHA}}}
	}
	for _, forged := range []controllerwire.CreateRunRequest{request("gh:701", "victim/app"), request("gh:701", "https://github.com/victim/app"), request("gh:701", "acme/other"), request("gh:999", "victim/app"), request("git:git.example.test/acme/widgets", "github.com/victim/app")} {
		if code := f.call("POST", "/api/v1/runs", olga.auth, forged, nil); code != http.StatusForbidden {
			t.Fatalf("forged GitHub identity %+v = %d, want 403", forged.Request.Git.Repository, code)
		}
	}
	for _, key := range []string{"gh:701", "git:git.example.test/acme/widgets"} {
		upload := map[string]any{"digest": strings.Repeat("a", 64), "size": 1, "git": map[string]any{"repository": map[string]any{"key": key, "display": "github.com/victim/app"}, "sha": headSHA}, "source": map[string]any{"ref": headSHA}}
		if code := f.call("POST", "/api/v1/data/source", olga.auth, upload, nil); code != http.StatusForbidden {
			t.Fatalf("source upload of %s with a forged display = %d, want 403", key, code)
		}
	}
	var created controllerwire.CreateRunResponse
	if code := f.call("POST", "/api/v1/runs", olga.auth, request("gh:701", ""), &created); code != http.StatusCreated || created.Request.Request.Git.Repository.Display != "acme/widgets" {
		t.Fatalf("unlabelled GitHub identity = %d display=%q, want acme/widgets", code, created.Request.Request.Git.Repository.Display)
	}
	var uncovered controllerwire.CreateRunResponse
	if code := f.call("POST", "/api/v1/runs", olga.auth, request("gh:999", ""), &uncovered); code != http.StatusCreated || uncovered.Request.Request.Git.Repository.Display != "gh:999" {
		t.Fatalf("uncovered GitHub identity = %d display=%q, want the key", code, uncovered.Request.Request.Git.Repository.Display)
	}
	var keyed controllerwire.CreateRunResponse
	if code := f.call("POST", "/api/v1/runs", olga.auth, request("git:git.example.test/acme/widgets", ""), &keyed); code != http.StatusCreated || keyed.Request.Request.Git.Repository.Display != "git.example.test/acme/widgets" {
		t.Fatalf("keyed git identity = %d display=%q, want the key's path", code, keyed.Request.Request.Git.Repository.Display)
	}
	var victim struct {
		Capability string `json:"capability"`
	}
	if code := f.call("POST", "/api/v1/runners", olga.auth, map[string]any{"name": "victim-box", "repos": []string{"github.com/victim/*"}}, &victim); code != http.StatusCreated {
		t.Fatalf("enroll victim-scoped runner = %d", code)
	}
	for _, run := range []string{created.RunID, uncovered.RunID, keyed.RunID} {
		if code := f.call("POST", "/api/v1/runners/claim", "Bearer "+victim.Capability, githubClaimBody(capability.UnitTrigger, run, ""), nil); code != http.StatusForbidden {
			t.Fatalf("victim-scoped runner claim of another repository's run %s = %d, want 403", run, code)
		}
	}
}

func TestGitHubAppRunsAnywhereUnlessTheRepositoryPinsCloud(t *testing.T) {
	f := newDeploymentAppFixture(t, deploymenttest.Cloud(t, "https://controller.test"))
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for pipeline, runner := range map[string]string{"any-build": "", "cloud-build": "cloud"} {
		if code := f.subscribe(olga, "acme/widgets", pipeline, map[string]any{"runner": runner}); code != http.StatusOK {
			t.Fatalf("subscribe %s runner=%q: %d", pipeline, runner, code)
		}
	}
	payload := pushPayload(7, 701, "acme/widgets", headSHA)
	payload["sender"] = map[string]any{"id": 501, "login": "olga"}
	code, out := f.deliver("push", payload, "")
	ids := startedRunIDs(t, out)
	if code != http.StatusAccepted || len(ids) != 2 {
		t.Fatalf("push = %d %v, want two runs", code, out)
	}
	anywhere := ""
	for _, id := range ids {
		sealed := f.sealedRequest(id)
		pinned := strings.Contains(strings.Join(sealed.Options.Requires.Value, ","), "class=cloud")
		if sealed.Dispatch != runspec.DispatchController || pinned != (sealed.Pipeline == "cloud-build") {
			t.Fatalf("%s sealed dispatch=%s requires=%v", sealed.Pipeline, sealed.Dispatch, sealed.Options.Requires.Value)
		}
		if !pinned {
			anywhere = id
		}
	}
	var laptop struct {
		Capability string `json:"capability"`
	}
	if code := f.call("POST", "/api/v1/runners", olga.auth, map[string]any{"name": "olga-laptop", "repos": []string{"github.com/*/*"}, "personal": true}, &laptop); code != http.StatusCreated {
		t.Fatalf("enroll personal machine: %d", code)
	}
	var claimed controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", "Bearer "+laptop.Capability, githubClaimBody(capability.UnitPlan, "", ""), &claimed); code != http.StatusOK || claimed.Claim != nil {
		t.Fatalf("the sender's personal machine took an App run: %d %+v", code, claimed.Claim)
	}
	machine, _ := f.teamRunner(olga, "team-box")
	if code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitPlan, "", ""), &claimed); code != http.StatusOK || claimed.Claim == nil || claimed.Claim.RunID != anywhere || claimed.Claim.Unit != capability.UnitPlan {
		t.Fatalf("team runner plan claim = %d %+v, want %s", code, claimed.Claim, anywhere)
	}
	claimed = controllerwire.ClaimResponse{}
	if code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitPlan, "", ""), &claimed); code != http.StatusOK || claimed.Claim != nil {
		t.Fatalf("team runner took the Cloud-pinned plan: %d %+v", code, claimed.Claim)
	}
}
