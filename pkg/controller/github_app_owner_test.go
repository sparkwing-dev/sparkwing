package controller_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc/githuboidctest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
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

func TestGitHubAppPoolScheduleReachesItsReservedRunner(t *testing.T) {
	f := newAppFixture(t, func(s *controller.Server) *controller.Server { return s.WithCloudDeployment(true) })
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
	if code := f.call("POST", "/api/v1/runners/claim", "Bearer "+minted.Capability, githubClaimBody(capability.UnitTrigger, "", ""), &claimed); code != http.StatusOK || claimed.Claim == nil || claimed.Claim.RunID != started.RunID {
		t.Fatalf("prod runner claim of the pool schedule = %d, claimed=%v", code, claimed.Claim != nil)
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
	f := newAppFixture(t, func(s *controller.Server) *controller.Server { return s.WithCloudDeployment(true) })
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
		if sealed.Dispatch != runspec.DispatchController || pinned != (sealed.Pipeline == "cloud-build") || sealed.Git.App.Value.AuthorID != 0 {
			t.Fatalf("%s sealed dispatch=%s requires=%v sender=%d", sealed.Pipeline, sealed.Dispatch, sealed.Options.Requires.Value, sealed.Git.App.Value.AuthorID)
		}
		if !pinned {
			anywhere = id
		}
	}
	machine, _ := f.teamRunner(olga, "team-box")
	var claimed controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitPlan, "", ""), &claimed); code != http.StatusOK || claimed.Claim == nil || claimed.Claim.RunID != anywhere || claimed.Claim.Unit != capability.UnitPlan {
		t.Fatalf("team runner plan claim = %d %+v, want %s", code, claimed.Claim, anywhere)
	}
	claimed = controllerwire.ClaimResponse{}
	if code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitPlan, "", ""), &claimed); code != http.StatusOK || claimed.Claim != nil {
		t.Fatalf("team runner took the Cloud-pinned plan: %d %+v", code, claimed.Claim)
	}
}

func TestGitHubAppRunsReachAnApprovedPersonalMachineOnlyForItsAuthorWithAccess(t *testing.T) {
	for _, permission := range []string{"write", "none"} {
		t.Run(permission, func(t *testing.T) {
			f := newAppFixture(t, func(s *controller.Server) *controller.Server { return s.WithCloudDeployment(true) })
			olga := f.ghUser(501, "olga")
			f.connect(olga, 501, 7, acmeAdmin)
			if code := f.subscribe(olga, "acme/widgets", "any-build", map[string]any{"pull_request": true}); code != http.StatusOK {
				t.Fatalf("subscribe: %d", code)
			}
			var minted struct {
				Capability string `json:"capability"`
			}
			if code := f.call("POST", "/api/v1/runners", olga.auth, map[string]any{"name": "olga-laptop", "repos": []string{"github.com/*/*"}, "personal": true}, &minted); code != http.StatusCreated {
				t.Fatalf("enroll personal machine: %d", code)
			}
			laptop := "Bearer " + minted.Capability
			var state controllerwire.MachineAutomation
			if code := f.call("PUT", "/api/v1/runners/olga-laptop/automation/approval", olga.auth, map[string]any{"approved": true}, nil); code != http.StatusBadRequest {
				t.Fatalf("approval before a request = %d, want 400", code)
			}
			if code := f.call("PUT", "/api/v1/runners/olga-laptop/automation", olga.auth, map[string]any{}, nil); code != http.StatusBadRequest {
				t.Fatalf("request without a value = %d, want 400", code)
			}
			if code := f.call("PUT", "/api/v1/runners/olga-laptop/automation", olga.auth, map[string]any{"requested": true}, &state); code != http.StatusOK || state.State != "requested" {
				t.Fatalf("request = %d %+v", code, state)
			}
			if code := f.call("PUT", "/api/v1/runners/olga-laptop/automation/approval", olga.auth, map[string]any{"approved": true}, &state); code != http.StatusOK || state.State != "approved" {
				t.Fatalf("approve = %d %+v", code, state)
			}
			f.app.SetCollaborator("acme/widgets", 501, "olga", permission)
			laptopTakes := func(payload map[string]any, event string, want bool) []string {
				t.Helper()
				code, out := f.deliver(event, payload, "")
				ids := startedRunIDs(t, out)
				if code != http.StatusAccepted || len(ids) != 1 {
					t.Fatalf("%s = %d %v", event, code, out)
				}
				var claimed controllerwire.ClaimResponse
				if code := f.call("POST", "/api/v1/runners/claim", laptop, githubClaimBody(capability.UnitPlan, "", ""), &claimed); code != http.StatusOK || (claimed.Claim != nil) != want {
					t.Fatalf("%s: laptop claim = %d %+v, want claimed=%t", event, code, claimed.Claim, want)
				}
				return ids
			}
			pr := prPayload(7, 701, "acme/widgets", 701)
			pr["pull_request"].(map[string]any)["user"] = map[string]any{"id": 601, "login": "contributor"}
			pr["pull_request"].(map[string]any)["head"].(map[string]any)["repo"] = map[string]any{"id": 701, "full_name": "acme/widgets"}
			pr["sender"] = map[string]any{"id": 501, "login": "olga"}
			if ids := laptopTakes(pr, "pull_request", false); f.sealedRequest(ids[0]).Automation.Value.AuthorID != 601 {
				t.Fatal("the pull request's author was not sealed")
			}
			own := prPayload(7, 701, "acme/widgets", 701)
			own["pull_request"].(map[string]any)["user"] = map[string]any{"id": 501, "login": "olga"}
			own["pull_request"].(map[string]any)["head"].(map[string]any)["repo"] = map[string]any{"id": 701, "full_name": "acme/widgets"}
			own["pull_request"].(map[string]any)["head"].(map[string]any)["sha"] = strings.Repeat("b", 40)
			own["sender"] = map[string]any{"id": 601, "login": "contributor"}
			laptopTakes(own, "pull_request", false)
			reopened := prPayload(7, 701, "acme/widgets", 701)
			reopened["action"] = "reopened"
			reopened["pull_request"].(map[string]any)["user"] = map[string]any{"id": 501, "login": "olga"}
			reopened["pull_request"].(map[string]any)["head"].(map[string]any)["repo"] = map[string]any{"id": 701, "full_name": "acme/widgets"}
			reopened["pull_request"].(map[string]any)["head"].(map[string]any)["sha"] = strings.Repeat("d", 40)
			reopened["sender"] = map[string]any{"id": 501, "login": "olga"}
			laptopTakes(reopened, "pull_request", false)
			f.app.SetCommitAuthor("acme/widgets", headSHA, 501)
			spoofed := pushPayload(7, 701, "acme/widgets", headSHA)
			spoofed["sender"] = map[string]any{"id": 601, "login": "contributor"}
			laptopTakes(spoofed, "push", false)
			pushed := pushPayload(7, 701, "acme/widgets", headSHA)
			pushed["after"], pushed["sender"] = headSHA, map[string]any{"id": 501, "login": "olga"}
			pushed["head_commit"] = map[string]any{"id": headSHA, "timestamp": "2026-01-02T00:00:00Z"}
			ids := laptopTakes(pushed, "push", permission != "none")
			if sealed := f.sealedRequest(ids[0]).Automation.Value; sealed.SenderID != 501 || sealed.AuthorID != 501 || len(sealed.Repos) != 1 || sealed.Repos[0] != "gh:701" {
				t.Fatalf("push automation identity: %+v", sealed)
			}
			if permission == "none" {
				return
			}
			f.app.SetCollaborator("acme/widgets", 501, "olga", "none")
			next := strings.Repeat("c", 40)
			f.app.SetCommitAuthor("acme/widgets", next, 501)
			again := pushPayload(7, 701, "acme/widgets", next)
			again["after"], again["sender"] = next, map[string]any{"id": 501, "login": "olga"}
			again["head_commit"] = map[string]any{"id": next, "timestamp": "2026-01-02T00:01:00Z"}
			laptopTakes(again, "push", false)
		})
	}
}
