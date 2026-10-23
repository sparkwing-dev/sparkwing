package controller

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func (f *githubCompletionFixture) pending() func() {
	f.t.Helper()
	engineSQL(f.t, f.server.store, `UPDATE runs SET status='pending' WHERE team='a' AND id='run'`)
	engineSQL(f.t, f.server.store, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a','run','github_check','pending',1,1)`)
	var prefix string
	if err := teststore.SQL(f.t, f.server.store).QueryRow(`SELECT capability_prefix FROM triggers WHERE team='a' AND id='run'`).Scan(&prefix); err != nil {
		f.t.Fatal(err)
	}
	owner, err := f.server.store.AsOperator().ResolveAuthority(f.t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	if err != nil {
		f.t.Fatal(err)
	}
	actor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	return func() {
		f.t.Helper()
		if _, err := f.tenant.TransitionRun(f.t.Context(), runstate.RunTransition{RunID: "run", Actor: actor, To: runstate.RunRunning, Cause: runstate.RunStart}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *githubCompletionFixture) addReportingNode(id string, outcome runstate.NodeState, text string) {
	f.t.Helper()
	run, err := f.tenant.ReadRun(f.t.Context(), "run")
	if err != nil {
		f.t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(run.Plan, &plan); err != nil {
		f.t.Fatal(err)
	}
	plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: id})
	encoded, err := json.Marshal(plan)
	if err != nil {
		f.t.Fatal(err)
	}
	engineSQL(f.t, f.server.store, `UPDATE runs SET plan_json=? WHERE team='a' AND id='run'`, encoded)
	if err := teststore.SeedNode(f.server.store, f.t.Context(), store.Node{RunID: "run", NodeID: id, Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := teststore.SeedNodeFinish(f.server.store, f.t.Context(), "a", runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(id)}, runstate.NodeResult{Outcome: outcome, MaskedError: text}); err != nil {
		f.t.Fatal(err)
	}
}

func TestGitHubAppRunReportsOneCheckRun(t *testing.T) {
	f := newGitHubCompletion(t)
	start := f.pending()
	f.reconcile()
	checks := f.api.checks
	if len(checks) != 1 || checks[0].Status != "queued" || checks[0].Name != "sparkwing/ci" || checks[0].HeadSHA != strings.Repeat("a", 40) || checks[0].ExternalID != "run" || checks[0].DetailsURL != "https://dash.example.test/runs?run=run" {
		t.Fatalf("queued check=%+v", checks)
	}
	id, err := f.tenant.GitHubCheckRun(t.Context(), "run")
	if err != nil || id != 101 {
		t.Fatalf("check identity=%d: %v", id, err)
	}
	start()
	f.reconcile()
	f.addReportingNode("compile", runstate.NodeSuccess, "")
	f.finish(runstate.RunFailed)
	f.reconcile()
	checks = f.api.checks
	if len(checks) != 3 || checks[1].Status != "in_progress" || checks[2].Status != "completed" || checks[2].Conclusion != "failure" {
		t.Fatalf("lifecycle=%+v", checks)
	}
	if got := strings.Join(f.api.checkWrites, ","); got != "POST /repos/acme/app/check-runs,PATCH /repos/acme/app/check-runs/101,PATCH /repos/acme/app/check-runs/101" {
		t.Fatalf("check identity changed across provider writes: %s", got)
	}
	if got, err := f.tenant.GitHubCheckRun(t.Context(), "run"); err != nil || got != id {
		t.Fatalf("check identity changed=%d: %v", got, err)
	}
	for _, want := range []string{"Failed", "1 success", "1 failed", "https://dash.example.test/runs?run=run"} {
		if !strings.Contains(checks[2].Output.Summary, want) {
			t.Fatalf("summary lacks %q: %s", want, checks[2].Output.Summary)
		}
	}
	if len(f.api.statuses) != 0 {
		t.Fatalf("check run posted statuses=%+v", f.api.statuses)
	}
	for _, token := range f.api.tokens {
		if len(token.IDs) != 1 || token.IDs[0] != 42 || len(token.Names) != 0 || len(token.Permissions) != 1 || token.Permissions["checks"] != "write" {
			t.Fatalf("check token=%+v", token)
		}
	}
	desired, delivered, _, _ := f.revision()
	if desired != 3 || delivered != desired {
		t.Fatalf("revisions=%d/%d", delivered, desired)
	}
}

func TestGitHubAppWithoutChecksPermissionPostsStatuses(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.checksGranted = false
	var logs bytes.Buffer
	f.server.logger = slog.New(slog.NewTextHandler(&logs, nil))
	start := f.pending()
	f.reconcile()
	start()
	f.reconcile()
	f.finish(runstate.RunSuccess)
	f.reconcile()
	if len(f.api.checks) != 0 || len(f.api.statuses) != 2 || f.api.statuses[0].State != "pending" || f.api.statuses[1].State != "success" {
		t.Fatalf("fallback checks=%+v statuses=%+v", f.api.checks, f.api.statuses)
	}
	for _, c := range []struct {
		id      string
		granted bool
	}{{"second", false}, {"third", true}} {
		f.api.checksGranted = c.granted
		run, err := f.tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		run.Request.RunID = c.id
		run.Request.Request.Git.SHA = strings.Repeat(map[bool]string{false: "b", true: "c"}[c.granted], 40)
		run.Request.Request.Git.App.Value.SHA = run.Request.Request.Git.SHA
		request := sealWith(t, run.Request, appProvenance(t, run.Request.Request.Git.App.Value, run.Request.Request.PullRequest, "ci"))
		if err := teststore.SeedRun(f.server.store, t.Context(), "a", store.Run{ID: c.id, Pipeline: "ci", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		engineSQL(t, f.server.store, `UPDATE runs SET request_json=? WHERE team='a' AND id=?`, request, c.id)
		engineSQL(t, f.server.store, `INSERT INTO triggers(team,id,unit_kind,created_at,available_at,expires_at,needs_labels,prefers_labels) VALUES ('a',?,'trigger',1,1,1,?,?)`, c.id, []byte(`[]`), []byte(`[]`))
		engineSQL(t, f.server.store, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a',?,'github_check','pending',1,1)`, c.id)
		f.reconcile()
	}
	if len(f.api.statuses) != 3 || f.api.statuses[2].State != "pending" || len(f.api.checks) != 1 || f.api.checks[0].Status != "queued" || f.api.checks[0].ExternalID != "third" || f.api.checks[0].HeadSHA != strings.Repeat("c", 40) {
		t.Fatalf("permission acceptance checks=%+v statuses=%+v", f.api.checks, f.api.statuses)
	}
	if got := strings.Join(f.api.statusSHAs, ","); got != strings.Repeat("a", 40)+","+strings.Repeat("a", 40)+","+strings.Repeat("b", 40) {
		t.Fatalf("fallback reported the wrong commits: %s", got)
	}
	for _, status := range f.api.statuses {
		if status.Context != "sparkwing/ci" {
			t.Fatalf("status context=%q", status.Context)
		}
	}
	if n := strings.Count(logs.String(), "has not accepted the checks permission"); n != 1 {
		t.Fatalf("permission warning count=%d: %s", n, logs.String())
	}
	for _, token := range f.api.tokens {
		if len(token.IDs) != 1 || token.IDs[0] != 42 || len(token.Names) != 0 || len(token.Permissions) != 1 || token.Permissions["checks"] != "write" && token.Permissions["statuses"] != "write" {
			t.Fatalf("fallback token=%+v", token)
		}
	}
}

func TestGitHubAppCheckRunSummaryIsBounded(t *testing.T) {
	f := newGitHubCompletion(t)
	for i := range 400 {
		f.addReportingNode("node-"+strings.Repeat("x", 40)+"-"+strconv.Itoa(i), runstate.NodeFailed, strings.Repeat("é", 500))
	}
	f.finish(runstate.RunFailed)
	engineSQL(t, f.server.store, `UPDATE runs SET error=? WHERE team='a' AND id='run'`, strings.Repeat("ü", 100_000))
	f.reconcile()
	if len(f.api.checks) != 1 || f.api.checks[0].Status != "completed" {
		t.Fatalf("summary check=%+v", f.api.checks)
	}
	summary := f.api.checks[0].Output.Summary
	if !utf8.ValidString(summary) || utf8.RuneCountInString(summary) > 65535 || !strings.Contains(summary, "401 failed") || !strings.Contains(summary, "https://dash.example.test/runs?run=run") {
		t.Fatalf("summary lost bounds, counts or link: %s", summary)
	}
}

func TestGitHubAppCheckRunSummaryKeepsPrivateRunDataInConsole(t *testing.T) {
	f := newGitHubCompletion(t)
	f.addReportingNode("secret-node-name", runstate.NodeFailed, "ghp_secret-looking-node-error")
	f.finish(runstate.RunFailed)
	engineSQL(t, f.server.store, `UPDATE runs SET error='ghp_secret-looking-run-error' WHERE team='a' AND id='run'`)
	f.reconcile()
	if len(f.api.checks) != 1 {
		t.Fatalf("summary check=%+v", f.api.checks)
	}
	summary := f.api.checks[0].Output.Summary
	for _, private := range []string{"secret-node-name", "ghp_secret-looking-node-error", "ghp_secret-looking-run-error"} {
		if strings.Contains(summary, private) {
			t.Fatalf("public check exposes %q: %s", private, summary)
		}
	}
	for _, want := range []string{"Failed", "2 failed", "https://dash.example.test/runs?run=run"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary lacks %q: %s", want, summary)
		}
	}
}
