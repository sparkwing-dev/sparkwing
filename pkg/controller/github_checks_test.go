package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f *appFixture) drainChecks() {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := controller.DrainGitHubChecks(ctx, f.srv); err != nil {
		f.t.Fatalf("drain check runs: %v", err)
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

// pushRun delivers a push of sha and returns the one run it started.
func (f *appFixture) pushRun(sha string) string {
	f.t.Helper()
	code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", sha), "")
	ids := startedRunIDs(f.t, out)
	if code != http.StatusAccepted || len(ids) != 1 {
		f.t.Fatalf("push = %d %v, want one run", code, out)
	}
	return ids[0]
}

// claimedRun is a runner holding a claim on a run's trigger.
type claimedRun struct {
	auth       string
	generation int64
}

// startRun claims runID's trigger with a runner of owner's team and records
// the run as running, the way a runner does.
func (f *appFixture) startRun(owner signedIn, runID string) claimedRun {
	f.t.Helper()
	var m mintedRunner
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth,
		map[string]any{"name": "s-" + runID, "repos": []string{"github.com/*/*"}}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint runner = %d", code)
	}
	var claimed store.Trigger
	c := claimedRun{auth: "Bearer " + m.Token}
	if code := f.call("POST", "/api/v1/triggers/"+runID+"/claim", c.auth, nil, &claimed); code != http.StatusOK {
		f.t.Fatalf("claim trigger %s = %d", runID, code)
	}
	c.generation = claimed.ClaimSeq
	if code := f.runnerCall(c, "POST", "/api/v1/runs", map[string]any{
		"id": runID, "pipeline": claimed.Pipeline, "status": "running", "started_at": time.Now().UTC(),
	}); code != http.StatusCreated {
		f.t.Fatalf("start run %s = %d", runID, code)
	}
	return c
}

func (f *appFixture) runnerCall(c claimedRun, method, path string, body any) int {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req, err := http.NewRequest(method, f.url+path, bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.auth)
	req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(c.generation, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		f.t.Logf("%s %s = %d: %s", method, path, resp.StatusCode, msg)
	}
	return resp.StatusCode
}

func (f *appFixture) finishRun(c claimedRun, runID, status, errMsg string) {
	f.t.Helper()
	if code := f.runnerCall(c, "POST", "/api/v1/runs/"+runID+"/finish",
		map[string]any{"status": status, "error": errMsg}); code != http.StatusNoContent {
		f.t.Fatalf("finish run %s = %d", runID, code)
	}
}

func (f *appFixture) addNode(runID, nodeID, outcome, errMsg string) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.store.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.FinishNode(ctx, runID, nodeID, outcome, errMsg, nil); err != nil {
		f.t.Fatal(err)
	}
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

func onlyCheckTokens(t *testing.T, app *githubapptest.GitHub) {
	t.Helper()
	for _, m := range app.Minted() {
		if _, checks := m.Permissions["checks"]; checks &&
			(len(m.Repositories) != 1 || m.Repositories[0] != "widgets" || len(m.Permissions) != 1 || m.Permissions["checks"] != "write") {
			t.Fatalf("check run token request = %+v, want widgets with checks:write only", m)
		}
	}
}

// A run the App started is one check run on its commit that moves from
// queued through in_progress to completed, and posts no commit status.
func TestGitHubAppRunReportsOneCheckRun(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	runID := f.pushRun(headSHA)
	f.drainChecks()

	calls := f.app.CheckRunCalls()
	if len(calls) != 1 {
		t.Fatalf("check run writes after the push = %+v, want one create", calls)
	}
	created := calls[0]
	if created.Method != "create" || created.Name != "sparkwing/build" || created.HeadSHA != headSHA ||
		created.Status != "queued" || created.ExternalID != runID ||
		created.DetailsURL != "https://dash.example.com/runs?run="+runID {
		t.Fatalf("create = %+v", created)
	}

	runner := f.startRun(olga, runID)
	f.drainChecks()
	f.addNode(runID, "compile", "success", "")
	f.addNode(runID, "unit-tests", "failed", "3 tests failed")
	f.finishRun(runner, runID, "failed", "node unit-tests failed")
	f.drainChecks()

	calls = f.app.CheckRunCalls()
	if len(calls) != 3 {
		t.Fatalf("check run writes = %+v, want create, in_progress and completed", calls)
	}
	started, done := calls[1], calls[2]
	if started.Method != "update" || started.ID != created.ID || started.Status != "in_progress" {
		t.Fatalf("second write = %+v, want in_progress on check run %d", started, created.ID)
	}
	if done.Method != "update" || done.ID != created.ID || done.Status != "completed" || done.Conclusion != "failure" {
		t.Fatalf("third write = %+v, want completed failure on check run %d", done, created.ID)
	}
	for _, want := range []string{"compile", "unit-tests", "3 tests failed", "https://dash.example.com/runs?run=" + runID} {
		if !strings.Contains(done.Summary, want) {
			t.Fatalf("summary lacks %q:\n%s", want, done.Summary)
		}
	}
	if got := f.app.Statuses(); len(got) != 0 {
		t.Fatalf("the App posted commit statuses %+v; its runs report as check runs", got)
	}
	onlyCheckTokens(t, f.app)
}

// Each of Sparkwing's run outcomes lands as the conclusion that names it.
func TestGitHubAppCheckRunConclusions(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	for i, c := range []struct{ status, conclusion string }{
		{"success", "success"}, {"failed", "failure"}, {"cancelled", "cancelled"},
	} {
		runID := f.pushRun(strings.Repeat(string(rune('a'+i)), 40))
		f.finishRun(f.startRun(olga, runID), runID, c.status, "")
		f.drainChecks()
		calls := f.app.CheckRunCalls()
		last := calls[len(calls)-1]
		if last.ExternalID != runID || last.Status != "completed" || last.Conclusion != c.conclusion {
			t.Fatalf("run finished %s: last write %+v, want conclusion %s", c.status, last, c.conclusion)
		}
	}
}

// GitHub failing a check run write is retried, and a write that never lands
// neither fails the delivery nor stops the run.
func TestGitHubAppCheckRunSurvivesGitHubErrors(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)

	f.app.FailCheckRuns(2)
	f.pushRun(headSHA)
	f.drainChecks()
	if calls := f.app.CheckRunCalls(); len(calls) != 1 || calls[0].Status != "queued" {
		t.Fatalf("after two 502s the writes = %+v, want the queued create retried into place", calls)
	}

	f.app.FailCheckRuns(1000)
	runID := f.pushRun(strings.Repeat("5", 40))
	f.drainChecks()
	if n := len(f.app.CheckRunCalls()); n != 1 {
		t.Fatalf("a GitHub that answers only 502 recorded %d writes", n)
	}
	if !strings.Contains(f.logs.String(), "github check run update failed") {
		t.Fatalf("a check run given up on logged nothing:\n%s", f.logs.String())
	}
	f.app.FailCheckRuns(0)
	f.finishRun(f.startRun(olga, runID), runID, "success", "")
	f.drainChecks()
	calls := f.app.CheckRunCalls()
	if last := calls[len(calls)-1]; last.ExternalID != runID || last.Conclusion != "success" {
		t.Fatalf("the run whose create failed ended with writes %+v, want a completed check run", calls)
	}
}

// An installation whose owner has not accepted the checks permission keeps
// getting commit statuses until it does, and says so in the log once.
func TestGitHubAppWithoutChecksPermissionPostsStatuses(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.app.SetChecksGranted(7, false)

	first := f.pushRun(headSHA)
	f.drainChecks()
	f.finishRun(f.startRun(olga, first), first, "success", "")
	f.pushRun(strings.Repeat("6", 40))
	f.drainChecks()

	if calls := f.app.CheckRunCalls(); len(calls) != 0 {
		t.Fatalf("check runs %+v on an installation without the permission", calls)
	}
	var states []string
	for _, st := range f.app.Statuses() {
		if st.Context != "sparkwing/build" {
			t.Fatalf("status %+v, want context sparkwing/build", st)
		}
		states = append(states, st.SHA[:1]+":"+st.State)
	}
	if strings.Join(states, ",") != "8:pending,8:success,6:pending" {
		t.Fatalf("statuses = %v, want pending and success on the first commit, pending on the second", states)
	}
	if n := strings.Count(f.logs.String(), "has not accepted the checks permission"); n != 1 {
		t.Fatalf("the missing permission was logged %d times, want once:\n%s", n, f.logs.String())
	}

	f.app.SetChecksGranted(7, true)
	f.pushRun(strings.Repeat("7", 40))
	f.drainChecks()
	if calls := f.app.CheckRunCalls(); len(calls) != 1 || calls[0].Status != "queued" {
		t.Fatalf("after the owner accepted, writes = %+v, want a check run", calls)
	}
	if n := len(f.app.Statuses()); n != 3 {
		t.Fatalf("after the owner accepted, %d statuses, want the 3 from before", n)
	}
}

// The summary stays within GitHub's limit however many nodes a run has and
// however long its error is.
func TestGitHubAppCheckRunSummaryIsBounded(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	runID := f.pushRun(headSHA)
	runner := f.startRun(olga, runID)
	for i := 0; i < 400; i++ {
		f.addNode(runID, "node-"+strings.Repeat("x", 40)+"-"+strconv.Itoa(i), "failed", strings.Repeat("é", 500))
	}
	f.finishRun(runner, runID, "failed", strings.Repeat("ü", 100_000))
	f.drainChecks()
	calls := f.app.CheckRunCalls()
	done := calls[len(calls)-1]
	if done.Status != "completed" {
		t.Fatalf("writes = %d, last %+v; the fake refuses a summary over the limit", len(calls), done.Status)
	}
	if n := utf8.RuneCountInString(done.Summary); n > 65535 {
		t.Fatalf("summary is %d characters", n)
	}
	if !utf8.ValidString(done.Summary) {
		t.Fatal("summary was cut inside a character")
	}
	if !strings.Contains(done.Summary, "more nodes") || !strings.Contains(done.Summary, "https://dash.example.com/runs?run="+runID) {
		t.Fatalf("summary does not say nodes were left out or lost its link:\n%s", done.Summary[len(done.Summary)-500:])
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
	f.drainChecks()
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
	trig, err := f.store.GetTrigger(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if trig.Pipeline != "build" || trig.GitSHA != headSHA || trig.GitBranch != "main" || trig.GithubRepo != "widgets" {
		t.Fatalf("re-run trigger = %+v, want build on main at the same commit", trig)
	}
	if _, out := f.deliver("check_run", rerun, ""); out["status"] != "duplicate" {
		t.Fatalf("redelivered rerequested = %v, want duplicate", out)
	}
	if n := len(f.triggers(olga.team)); n != 3 {
		t.Fatalf("the team has %d runs, want the push's 2 and one re-run", n)
	}
	f.drainChecks()
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

// A re-run is refused whatever else about it is in order when the commit is
// a fork's pull request head, when Sparkwing never ran the commit, or when the
// check run is not this App's.
func TestGitHubAppRerequestedForkOrForeignCommitStartsNothing(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", map[string]any{"push": true, "pull_request": true})
	f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	f.drainChecks()
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
	f.drainChecks()
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
	if code := f.call("DELETE", "/api/v1/github-app/installations/7", "Bearer "+f.admin, nil, nil); code != http.StatusNoContent {
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
	f.drainChecks()
	if code, _ := f.deliver("check_run", checkRunPayload(7, "rerequested", f.app.CheckRunCalls()[0]), ""); code != http.StatusTooManyRequests {
		t.Fatalf("rerequested past the team's cap = %d, want 429", code)
	}
}
