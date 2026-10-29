package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

func (f *appFixture) reconcileChecks() {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.srv.ReconcileRunEffects(ctx); err != nil {
		f.t.Fatalf("reconcile check runs: %v", err)
	}
}

func startedRunIDs(t *testing.T, out map[string]any) []string {
	t.Helper()
	runs, _ := out["runs"].([]any)
	var ids []string
	for _, r := range runs {
		run := r.(map[string]any)
		if run["status"] == "dispatched" {
			ids = append(ids, run["run_id"].(string))
		}
	}
	return ids
}

func (f *appFixture) pushRun(sha string) string {
	f.t.Helper()
	code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", sha), "")
	ids := startedRunIDs(f.t, out)
	if code != http.StatusAccepted || len(ids) != 1 {
		f.t.Fatalf("push = %d %v, want one run", code, out)
	}
	return ids[0]
}

func checkRunPayload(installation int64, action string, call githubapptest.CheckRunCall) map[string]any {
	return map[string]any{
		"action": action,
		"check_run": map[string]any{
			"id": call.ID, "name": call.Name, "head_sha": call.HeadSHA, "external_id": call.ExternalID,
			"status": "completed", "app": map[string]any{"id": githubapptest.AppID},
			"check_suite":   map[string]any{"id": 55, "head_branch": "main"},
			"pull_requests": []any{},
		},
		"installation": map[string]any{"id": installation},
		"repository":   map[string]any{"id": 701, "full_name": "acme/widgets"},
		"sender":       map[string]any{"login": "olga"},
	}
}

func checkSuitePayload(installation int64, action, sha string, pullRequests []any) map[string]any {
	return map[string]any{
		"action": action,
		"check_suite": map[string]any{
			"id": 55, "head_branch": "main", "head_sha": sha,
			"app": map[string]any{"id": githubapptest.AppID}, "pull_requests": pullRequests,
		},
		"installation": map[string]any{"id": installation},
		"repository":   map[string]any{"id": 701, "full_name": "acme/widgets"},
		"sender":       map[string]any{"login": "olga"},
	}
}

// Re-running a check run from GitHub starts that pipeline again on the same
// commit, once however often the delivery arrives.
func TestGitHubAppCheckRunRerequestedRerunsItsPipeline(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.subscribe(olga, "acme/widgets", "test", nil)
	f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	f.reconcileChecks()
	var build githubapptest.CheckRunCall
	for _, c := range f.app.CheckRunCalls() {
		if c.Name == "sparkwing/build" {
			build = c
		}
	}

	for _, action := range []string{"created", "completed", "requested_action"} {
		if _, out := f.deliver("check_run", checkRunPayload(7, action, build), ""); out["status"] != "ignored" {
			t.Fatalf("check_run %s = %v, want ignored", action, out)
		}
	}
	rerun := checkRunPayload(7, "rerequested", build)
	code, out := f.deliver("check_run", rerun, "")
	ids := startedRunIDs(t, out)
	if code != http.StatusAccepted || len(ids) != 1 {
		t.Fatalf("rerequested = %d %v, want one run", code, out)
	}
	trig, err := f.trigger(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if trig.Pipeline != "build" || trig.GitSHA != headSHA || trig.GitBranch != "refs/heads/main" || trig.Repo != "acme/widgets" {
		t.Fatalf("re-run trigger = %+v, want build on main at the same commit", trig)
	}
	if _, out := f.deliver("check_run", rerun, ""); out["status"] != "queued" {
		t.Fatalf("redelivered rerequested = %v, want inbox acknowledgement", out)
	}
	if n := len(f.triggers(olga.team)); n != 3 {
		t.Fatalf("the team has %d runs, want the push's 2 and one re-run", n)
	}
	f.reconcileChecks()
	calls := f.app.CheckRunCalls()
	if last := calls[len(calls)-1]; last.Method != "create" || last.Name != "sparkwing/build" || last.ExternalID != ids[0] {
		t.Fatalf("the re-run's check run = %+v, want a new one", last)
	}
}

// Re-running the whole suite starts every pipeline the team subscribes to the
// repository; GitHub asking for a new suite starts nothing, because the push
// already did.
func TestGitHubAppCheckSuiteRerequestedRerunsEveryPipeline(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.subscribe(olga, "acme/widgets", "test", nil)
	f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")

	for _, action := range []string{"requested", "completed"} {
		if _, out := f.deliver("check_suite", checkSuitePayload(7, action, headSHA, nil), ""); out["status"] != "ignored" {
			t.Fatalf("check_suite %s = %v, want ignored", action, out)
		}
	}
	if n := len(f.triggers(olga.team)); n != 2 {
		t.Fatalf("check_suite requested started runs: the team has %d, want 2", n)
	}
	code, out := f.deliver("check_suite", checkSuitePayload(7, "rerequested", headSHA, nil), "")
	if ids := startedRunIDs(t, out); code != http.StatusAccepted || len(ids) != 2 {
		t.Fatalf("suite rerequested = %d %v, want two runs", code, out)
	}
	if n := len(f.triggers(olga.team)); n != 4 {
		t.Fatalf("the team has %d runs, want 4", n)
	}
}

func TestGitHubAppCheckSuiteRerequestedNeedsEachPipelinesOwnRun(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.pushRun(headSHA)
	f.subscribe(olga, "acme/widgets", "test", nil)
	code, out := f.deliver("check_suite", checkSuitePayload(7, "rerequested", headSHA, nil), "")
	if ids := startedRunIDs(t, out); code != http.StatusAccepted || len(ids) != 1 {
		t.Fatalf("suite reran a pipeline with no prior App run: %d %v", code, out)
	}
	if runs := f.triggers(olga.team); len(runs) != 2 || runs[0].Pipeline != "build" {
		t.Fatalf("runs after suite rerequest = %+v, want only build rerun", runs)
	}
}

func TestGitHubAppRerequestedNeedsPriorEventsSubscription(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.pushRun(headSHA)
	f.reconcileChecks()
	build := f.app.CheckRunCalls()[0]
	f.subscribe(olga, "acme/widgets", "build", map[string]any{"push": false, "pull_request": true})
	for _, event := range []string{"check_run", "check_suite"} {
		var payload map[string]any
		if event == "check_run" {
			payload = checkRunPayload(7, "rerequested", build)
		} else {
			payload = checkSuitePayload(7, "rerequested", headSHA, nil)
		}
		if _, out := f.deliver(event, payload, ""); out["status"] != "ignored" {
			t.Fatalf("%s reran a push after push subscription was removed: %v", event, out)
		}
	}
}

func TestGitHubAppRerequestedUsesCurrentBranchFilter(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.pushRun(headSHA)
	f.reconcileChecks()
	build := f.app.CheckRunCalls()[0]
	if code := f.subscribe(olga, "acme/widgets", "build", map[string]any{"branches": []string{"release/*"}}); code != http.StatusOK {
		t.Fatalf("update subscription = %d", code)
	}
	for _, event := range []string{"check_run", "check_suite"} {
		var payload map[string]any
		if event == "check_run" {
			payload = checkRunPayload(7, "rerequested", build)
		} else {
			payload = checkSuitePayload(7, "rerequested", headSHA, nil)
		}
		if code, out := f.deliver(event, payload, ""); code != http.StatusAccepted || out["status"] != "ignored" {
			t.Fatalf("%s reran a branch excluded by the updated filter: %d %v", event, code, out)
		}
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("rerun created %d extra runs", n-1)
	}
}

// A re-run is refused whatever else about it is in order when the commit is
// a fork's pull request head, when Sparkwing never ran the commit, or when the
// check run is not this App's.
func TestGitHubAppRerequestedForkOrForeignCommitStartsNothing(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", map[string]any{"push": true, "pull_request": true})
	f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	f.reconcileChecks()
	build := f.app.CheckRunCalls()[0]

	fork := []any{map[string]any{
		"number": 3,
		"head":   map[string]any{"ref": "feature", "sha": headSHA, "repo": map[string]any{"id": 999}},
		"base":   map[string]any{"ref": "main", "sha": strings.Repeat("1", 40), "repo": map[string]any{"id": 701}},
	}}
	if _, out := f.deliver("check_suite", checkSuitePayload(7, "rerequested", headSHA, fork), ""); out["status"] != "ignored" {
		t.Fatalf("suite rerequested for a fork head = %v, want ignored", out)
	}
	forkRun := checkRunPayload(7, "rerequested", build)
	forkRun["check_run"].(map[string]any)["pull_requests"] = fork
	if _, out := f.deliver("check_run", forkRun, ""); out["status"] != "ignored" {
		t.Fatalf("check run rerequested for a fork head = %v, want ignored", out)
	}
	unnamed := []any{map[string]any{
		"number": 3,
		"head":   map[string]any{"ref": "feature", "sha": headSHA, "repo": map[string]any{}},
		"base":   map[string]any{"ref": "main", "sha": strings.Repeat("1", 40), "repo": map[string]any{}},
	}}
	if _, out := f.deliver("check_suite", checkSuitePayload(7, "rerequested", headSHA, unnamed), ""); out["status"] != "ignored" {
		t.Fatalf("suite rerequested for a PR whose repositories carry no id = %v, want ignored", out)
	}
	never := strings.Repeat("9", 40)
	if _, out := f.deliver("check_suite", checkSuitePayload(7, "rerequested", never, nil), ""); out["status"] != "ignored" {
		t.Fatalf("suite rerequested for a commit never run = %v, want ignored", out)
	}
	foreign := checkRunPayload(7, "rerequested", build)
	foreign["check_run"].(map[string]any)["app"] = map[string]any{"id": 1}
	if _, out := f.deliver("check_run", foreign, ""); out["status"] != "ignored" {
		t.Fatalf("rerequested for another App's check run = %v, want ignored", out)
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("refused re-runs started runs: the team has %d, want the push's 1", n)
	}
	if _, out := f.deliver("check_suite", checkSuitePayload(7, "rerequested", headSHA, nil), ""); out["status"] != "dispatched" {
		t.Fatalf("control: suite rerequested for the pushed commit = %v, want dispatched", out)
	}
}

// A re-run needs the installation bound to the team that ran the commit, and
// the check run it names must be that team's.
func TestGitHubAppRerequestedNeedsTheBinding(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.connect(bob, 502, 8, nil)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	f.reconcileChecks()
	build := f.app.CheckRunCalls()[0]

	if _, out := f.deliver("check_run", checkRunPayload(99, "rerequested", build), ""); out["status"] != "ignored" {
		t.Fatalf("rerequested through an unbound installation = %v, want ignored", out)
	}
	if _, out := f.deliver("check_run", checkRunPayload(8, "rerequested", build), ""); out["status"] != "ignored" {
		t.Fatalf("rerequested through bob's installation for olga's run = %v, want ignored", out)
	}
	if n := len(f.triggers(bob.team)); n != 0 {
		t.Fatalf("bob's team got %d runs", n)
	}
	if code := f.call("DELETE", "/api/v1/operator/github-app/installations/7", f.consoleAuth(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("operator unbind = %d", code)
	}
	if _, out := f.deliver("check_run", checkRunPayload(7, "rerequested", build), ""); out["status"] != "ignored" {
		t.Fatalf("rerequested after the installation was unbound = %v, want ignored", out)
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("olga's team has %d runs, want the push's 1", n)
	}
}

// A re-run spends the team's hourly budget like a push.
func TestGitHubAppRerequestedSpendsTheRunBudget(t *testing.T) {
	f := newAppFixture(t)
	f.srv.WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 1})
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.pushRun(headSHA)
	f.reconcileChecks()
	if code, _ := f.deliver("check_run", checkRunPayload(7, "rerequested", f.app.CheckRunCalls()[0]), ""); code != http.StatusTooManyRequests {
		t.Fatalf("rerequested past the team's cap = %d, want 429", code)
	}
}

func TestGitHubAppRerequestedRetainsTagAndPullRequestFilters(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.4s measured in the short class; the full suite runs it")
	}
	for _, kind := range []string{"tag", "pr base", "pr label"} {
		t.Run(kind, func(t *testing.T) {
			f := newAppFixture(t)
			owner := f.ghUser(501, "olga")
			f.connect(owner, 501, 7, acmeAdmin)
			event := "pull_request"
			payload := prPayload(7, 701, "acme/widgets", 701)
			subscription := map[string]any{"push": false, "pull_request": true, "base_branches": []string{"main"}}
			switch kind {
			case "tag":
				event = "push"
				payload = pushPayload(7, 701, "acme/widgets", headSHA)
				payload["ref"] = "refs/tags/v1.0"
				subscription = map[string]any{"push": false, "tags": []string{"v*"}}
			case "pr label":
				payload["action"] = "labeled"
				payload["label"] = map[string]any{"name": "ship"}
				subscription["pull_request_labeled"], subscription["pull_request_labels"] = true, []string{"ship"}
			}
			if code := f.subscribe(owner, "acme/widgets", "build", subscription); code != http.StatusOK {
				t.Fatalf("subscribe=%d", code)
			}
			_, first := f.deliver(event, payload, "")
			ids := startedRunIDs(t, first)
			if len(ids) != 1 {
				t.Fatalf("initial=%v", first)
			}
			original := f.sealedRequest(ids[0])
			f.reconcileChecks()
			calls := f.app.CheckRunCalls()
			if len(calls) != 1 {
				t.Fatalf("checks=%+v", calls)
			}
			_, rerun := f.deliver("check_run", checkRunPayload(7, "rerequested", calls[0]), "")
			ids = startedRunIDs(t, rerun)
			if len(ids) != 1 {
				t.Fatalf("rerun=%v", rerun)
			}
			got := f.sealedRequest(ids[0])
			if got.Git.Ref != original.Git.Ref || got.Git.SHA != original.Git.SHA || got.PullRequest != original.PullRequest || got.Git.App.Value.Event != original.Git.App.Value.Event {
				t.Fatalf("rerun facts=%+v original=%+v", got, original)
			}
		})
	}
}
