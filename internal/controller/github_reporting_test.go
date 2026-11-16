package controller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
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
	var plan nodewire.PlanDoc
	if err := json.Unmarshal(run.Plan, &plan); err != nil {
		f.t.Fatal(err)
	}
	plan.Jobs = append(plan.Jobs, nodewire.JobSpec{ID: id})
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

func TestGitHubAppWithoutChecksPermissionKeepsReportPending(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.checksGranted = false
	start := f.pending()
	f.reconcile()
	start()
	f.reconcile()
	f.finish(runstate.RunSuccess)
	f.reconcile()
	_, delivered, attempts, _ := f.revision()
	if len(f.api.checks) != 0 || delivered != 0 || attempts == 0 {
		t.Fatalf("missing checks permission: checks=%+v delivered=%d attempts=%d", f.api.checks, delivered, attempts)
	}
	for _, token := range f.api.tokens {
		if len(token.IDs) != 1 || token.IDs[0] != 42 || len(token.Names) != 0 || len(token.Permissions) != 1 || token.Permissions["checks"] != "write" {
			t.Fatalf("check token=%+v", token)
		}
	}
	f.api.checksGranted = true
	f.now = f.now.Add(time.Hour)
	f.reconcile()
	if len(f.api.checks) != 1 || f.api.checks[0].Status != "completed" || f.api.checks[0].Conclusion != "success" {
		t.Fatalf("permission acceptance checks=%+v", f.api.checks)
	}
	_, delivered, _, _ = f.revision()
	if delivered == 0 {
		t.Fatal("accepted Checks permission left report pending")
	}
}

func TestGitHubAppCheckRunSummaryIsBounded(t *testing.T) {
	f := newGitHubCompletion(t)
	run, err := f.tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	var plan nodewire.PlanDoc
	if err := json.Unmarshal(run.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	rows := make([]string, 400)
	args := make([]any, 0, 6*len(rows))
	for i := range rows {
		job := nodewire.JobSpec{ID: "node-" + strings.Repeat("x", 40) + "-" + strconv.Itoa(i)}
		plan.Jobs = append(plan.Jobs, job)
		spec, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = "('a','run',?,'done','failed',?,?,?,?,?)"
		args = append(args, job.ID, strings.Repeat("é", 500), f.now.UnixNano(), spec, []byte(`{}`), i+2)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, f.server.store, `UPDATE runs SET plan_json=? WHERE team='a' AND id='run'`, encoded)
	// perf: one insert projects the fixture's claim indexes once for the whole reporting batch.
	engineSQL(t, f.server.store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,error,finished_at,spec_json,execution_json,seq) VALUES `+strings.Join(rows, ","), args...)
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

type seededStep struct {
	id, status, err, summary string
	attempt                  int
	annotations              []nodewire.Annotation
}

func (f *githubCompletionFixture) report(job, summary string, annotations []nodewire.Annotation, attempt int, steps ...seededStep) {
	f.t.Helper()
	encoded := []byte(nil)
	if len(annotations) > 0 {
		var err error
		if encoded, err = json.Marshal(annotations); err != nil {
			f.t.Fatal(err)
		}
	}
	engineSQL(f.t, f.server.store, `UPDATE nodes SET summary=?,annotations_json=?,attempt=? WHERE team='a' AND run_id='run' AND node_id=?`, summary, encoded, attempt, job)
	at := f.now.UnixNano()
	seeded := map[int]bool{}
	for _, step := range steps {
		if !seeded[step.attempt] {
			seeded[step.attempt] = true
			engineSQL(f.t, f.server.store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','run',?,?,?,'fixture','fixture','process','fixture','local',?,?,'failed')`, job, step.attempt, step.attempt, at, at)
		}
		var stepAnnotations []byte
		if len(step.annotations) > 0 {
			var err error
			if stepAnnotations, err = json.Marshal(step.annotations); err != nil {
				f.t.Fatal(err)
			}
		}
		engineSQL(f.t, f.server.store, `INSERT INTO node_steps(team,run_id,node_id,attempt,generation,step_id,status,error,started_at,finished_at,annotations_json,summary) VALUES ('a','run',?,?,?,?,?,?,?,?,?,?)`, job, step.attempt, step.attempt, step.id, step.status, step.err, at, at, stepAnnotations, step.summary)
	}
}

func (f *githubCompletionFixture) completedCheck() githubapp.CheckRun {
	f.t.Helper()
	checks := f.api.checks
	if len(checks) == 0 || checks[len(checks)-1].Status != "completed" || checks[len(checks)-1].Output == nil {
		f.t.Fatalf("no completed check: %+v", checks)
	}
	return checks[len(checks)-1]
}

func TestGitHubAppCheckRunOnPublicRepositoryKeepsRunDetailsInConsole(t *testing.T) {
	f := newGitHubCompletion(t)
	f.addReportingNode("secret-node-name", runstate.NodeFailed, "ghp_secret-looking-node-error")
	f.report("secret-node-name", "secret-looking job summary", []nodewire.Annotation{{Level: "error", Message: "secret-looking annotation", File: "main.go", Line: 3}}, 1,
		seededStep{id: "secret-step", attempt: 1, status: "failed", err: "secret-looking step error", summary: "secret-looking step summary"})
	f.finish(runstate.RunFailed)
	engineSQL(t, f.server.store, `UPDATE runs SET error='ghp_secret-looking-run-error' WHERE team='a' AND id='run'`)
	f.reconcile()
	if len(f.api.checks) != 1 {
		t.Fatalf("summary check=%+v", f.api.checks)
	}
	out := f.api.checks[0].Output
	for _, private := range []string{"secret-node-name", "ghp_secret-looking-node-error", "ghp_secret-looking-run-error", "secret-looking job summary", "secret-step", "secret-looking step"} {
		if strings.Contains(out.Title+out.Summary+out.Text, private) {
			t.Fatalf("public repository check exposes %q: %+v", private, out)
		}
	}
	if len(out.Annotations) != 0 || len(f.api.annotations[101]) != 0 {
		t.Fatalf("public repository check carries details: %+v", out)
	}
	for _, want := range []string{"Failed", "2 failed", "https://dash.example.test/runs?run=run"} {
		if !strings.Contains(out.Summary, want) {
			t.Fatalf("summary lacks %q: %s", want, out.Summary)
		}
	}
}

func TestGitHubAppCheckRunCarriesDetailsOnPrivateRepositories(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	f.addReportingNode("compile", runstate.NodeSuccess, "")
	f.addReportingNode("unit-tests", runstate.NodeFailed, "go test: *** FAIL TestParse\nexpected 3, got 4")
	f.report("compile", "Binary size **12 MB**.", nil, 1)
	f.report("unit-tests", "", nil, 2,
		seededStep{id: "stale", attempt: 1, status: "failed", err: "stale attempt error", summary: "stale attempt summary"},
		seededStep{id: "vet", attempt: 2, status: "failed", err: "vet: *** shadows err", summary: "vet found 1 issue"})
	f.finish(runstate.RunFailed)
	engineSQL(t, f.server.store, `UPDATE runs SET error='1 job failed' WHERE team='a' AND id='run'`)
	f.reconcile()
	check := f.completedCheck()
	out := check.Output
	if out.Title != "Failed: 2 of 3 jobs failed" {
		t.Fatalf("title=%q", out.Title)
	}
	for _, want := range []string{"https://dash.example.test/runs?run=run", "**Job 3**: go test: *** FAIL TestParse", "### Job 2\n\nBinary size **12 MB**.", "**Job 3, step 1**: vet: *** shadows err", "### Job 3, step 1\n\nvet found 1 issue"} {
		if !strings.Contains(out.Summary, want) {
			t.Fatalf("summary lacks %q:\n%s", want, out.Summary)
		}
	}
	for _, want := range []string{"### Run\n\n```\n1 job failed", "### Job 3\n\n```\ngo test: *** FAIL TestParse\nexpected 3, got 4", "### Job 3, step 1\n\n```\nvet: *** shadows err"} {
		if !strings.Contains(out.Text, want) {
			t.Fatalf("text lacks %q:\n%s", want, out.Text)
		}
	}
	if strings.Contains(out.Summary+out.Text, "stale attempt") || strings.Contains(out.Summary+out.Text, "unit-tests") || strings.Contains(out.Summary+out.Text, "compile") {
		t.Fatalf("check carries a superseded attempt's results:\n%s\n%s", out.Summary, out.Text)
	}
}

func TestGitHubAppCheckRunDetailsAreBounded(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	run, err := f.tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	var plan nodewire.PlanDoc
	if err := json.Unmarshal(run.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	rows := make([]string, 400)
	args := make([]any, 0, 7*len(rows))
	for i := range rows {
		job := nodewire.JobSpec{ID: "node-" + strconv.Itoa(i)}
		plan.Jobs = append(plan.Jobs, job)
		spec, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = "('a','run',?,'done','failed',?,?,?,?,?,?)"
		failure := strings.Repeat("é", 300) + "\n````````\n" + strings.Repeat("ü", 300)
		summary := ""
		if i < 30 {
			summary = "```go\n" + strings.Repeat("界", 30_000)
		}
		args = append(args, job.ID, failure, f.now.UnixNano(), spec, []byte(`{}`), i+2, summary)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, f.server.store, `UPDATE runs SET plan_json=? WHERE team='a' AND id='run'`, encoded)
	// perf: one insert projects the fixture's claim indexes once for the whole reporting batch.
	engineSQL(t, f.server.store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,error,finished_at,spec_json,execution_json,seq,summary) VALUES `+strings.Join(rows, ","), args...)
	f.finish(runstate.RunFailed)
	engineSQL(t, f.server.store, `UPDATE runs SET error=? WHERE team='a' AND id='run'`, strings.Repeat("ü", 100_000))
	f.reconcile()
	out := f.completedCheck().Output
	for name, field := range map[string]string{"summary": out.Summary, "text": out.Text} {
		if !utf8.ValidString(field) || utf8.RuneCountInString(field) > 65535 {
			t.Fatalf("%s is out of bounds: %d runes", name, utf8.RuneCountInString(field))
		}
		if !strings.Contains(field, "truncated") || !strings.Contains(field, "https://dash.example.test/runs?run=run") {
			t.Fatalf("%s lacks its truncation note or link", name)
		}
		if fence := unclosedFence(field); fence != "" {
			t.Fatalf("%s leaves fence %q open", name, fence)
		}
	}
	if !strings.Contains(out.Summary, "401 failed") {
		t.Fatalf("summary lost its counts")
	}
}

// safety: judged apart from the reporter's own fence parser, so a parser bug cannot pass its own output.
func unclosedFence(doc string) string {
	open := ""
	for line := range strings.SplitSeq(doc, "\n") {
		trimmed := strings.TrimRight(line, " ")
		run := len(trimmed) - len(strings.TrimLeft(trimmed, "`"))
		switch {
		case open == "" && run >= 3:
			open = trimmed[:run]
		case open != "" && run >= len(open) && run == len(trimmed):
			open = ""
		}
	}
	return open
}

func pathAnnotations(n int, prefix string) []nodewire.Annotation {
	levels := []string{"error", "warn", "info", "debug", ""}
	out := make([]nodewire.Annotation, n)
	for i := range out {
		out[i] = nodewire.Annotation{Level: levels[i%len(levels)], Message: prefix + " finding " + strconv.Itoa(i), File: "./pkg/" + prefix + ".go", Line: i + 1, Column: 4}
		if i%3 == 1 {
			out[i].EndLine = i + 3
		}
		if i%7 == 6 {
			out[i].Line, out[i].EndLine, out[i].Column = 0, 0, 0
		}
	}
	return out
}

func TestGitHubAppCheckRunSendsPathAnnotationsInBatches(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	f.addReportingNode("lint", runstate.NodeFailed, "lint failed")
	unpathed := []nodewire.Annotation{{Level: "error", Message: "no file here"}, {Level: "warn", Message: "nor here", Line: 4}}
	f.report("lint", "", append(pathAnnotations(70, "job"), unpathed...), 1,
		seededStep{id: "vet", attempt: 1, status: "failed", err: "vet failed", annotations: append(pathAnnotations(50, "step"), unpathed...)})
	f.finish(runstate.RunFailed)
	f.reconcile()
	if got := fmt.Sprint(f.api.batches); got != "[50 50 20]" {
		t.Fatalf("annotation batches=%s", got)
	}
	sent := f.api.annotations[101]
	if len(sent) != 120 {
		t.Fatalf("sent %d annotations", len(sent))
	}
	levels := map[string]string{"error": "failure", "warn": "warning", "info": "notice", "debug": "notice", "": "notice"}
	for i, a := range sent {
		source, prefix := i, "job"
		if i >= 70 {
			source, prefix = i-70, "step"
		}
		want := pathAnnotations(70, prefix)[source]
		if a.Path != "pkg/"+prefix+".go" || a.Message != want.Message || a.Level != levels[want.Level] {
			t.Fatalf("annotation %d=%+v from %+v", i, a, want)
		}
		switch {
		case want.Line == 0:
			if a.StartLine != 1 || a.EndLine != 1 || a.StartColumn != 0 {
				t.Fatalf("unlined annotation %d=%+v", i, a)
			}
		case want.EndLine > want.Line:
			if a.StartLine != want.Line || a.EndLine != want.EndLine || a.StartColumn != 0 || a.EndColumn != 0 {
				t.Fatalf("range annotation %d=%+v", i, a)
			}
		default:
			if a.StartLine != want.Line || a.EndLine != want.Line || a.StartColumn != 4 || a.EndColumn != 4 {
				t.Fatalf("line annotation %d=%+v", i, a)
			}
		}
	}
	for _, check := range f.api.checks {
		if check.Status != "completed" || check.Conclusion != "failure" || check.Output.Title == "" || !strings.Contains(check.Output.Summary, "**Job 2**: lint failed") {
			t.Fatalf("annotation write lost the completed check: %+v", check)
		}
	}
}

func TestGitHubAppCheckRunCapsAnnotations(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	f.addReportingNode("lint", runstate.NodeFailed, "lint failed")
	f.report("lint", "", pathAnnotations(260, "job"), 1)
	f.finish(runstate.RunFailed)
	f.reconcile()
	if got := len(f.api.annotations[101]); got != 200 {
		t.Fatalf("sent %d annotations", got)
	}
	if summary := f.completedCheck().Output.Summary; !strings.Contains(summary, "60 more annotations") {
		t.Fatalf("summary does not count the unsent annotations:\n%s", summary)
	}
}

func TestGitHubAppCheckRunAnnotationsAreNotDuplicatedOnRedelivery(t *testing.T) {
	for name, recorded := range map[string]bool{"adopted": false, "recorded": true} {
		t.Run(name, func(t *testing.T) {
			f := newGitHubCompletion(t)
			f.api.private = true
			if recorded {
				start := f.pending()
				f.reconcile()
				start()
			}
			f.addReportingNode("lint", runstate.NodeFailed, "lint failed")
			f.report("lint", "", pathAnnotations(120, "job"), 1)
			f.finish(runstate.RunFailed)
			f.api.failWrite = f.api.writeCount + 2
			f.reconcile()
			if _, delivered, _, _ := f.revision(); delivered != 0 && !recorded {
				t.Fatal("a partly written check run was acknowledged")
			}
			f.now = f.now.Add(time.Hour)
			f.reconcile()
			sent := f.api.annotations[101]
			seen := map[string]bool{}
			for _, a := range sent {
				if seen[a.Message] {
					t.Fatalf("annotation %q sent twice", a.Message)
				}
				seen[a.Message] = true
			}
			if len(sent) != 120 {
				t.Fatalf("sent %d annotations", len(sent))
			}
			desired, delivered, _, _ := f.revision()
			if delivered != desired {
				t.Fatalf("revisions=%d/%d", delivered, desired)
			}
		})
	}
}

// safety: a stored secret read may occur in an accepted job or step ID and is masked only in text, so no ID may reach GitHub.
func TestGitHubAppCheckRunNamesNoJobOrStep(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	f.addReportingNode("ship-production", runstate.NodeFailed, "failed using ***")
	f.report("ship-production", "ran using ***", []nodewire.Annotation{{Level: "error", Message: "job finding", File: "***.go", Line: 3}}, 1,
		seededStep{id: "deploy-production", attempt: 1, status: "failed", err: "failed using ***", summary: "step ran using ***",
			annotations: []nodewire.Annotation{{Level: "info", Message: "failed using ***", File: "***.go", Line: 12}, {Level: "warn", File: "cmd/main.go", Line: 1}}})
	f.finish(runstate.RunFailed)
	f.reconcile()
	encoded, err := json.Marshal(f.api.checks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "production") {
		t.Fatalf("a job or step ID reached GitHub: %s", encoded)
	}
	out := f.completedCheck().Output
	for _, want := range []string{"### Job 2\n", "### Job 2, step 1\n", "**Job 2, step 1**: failed using ***"} {
		if !strings.Contains(out.Summary, want) {
			t.Fatalf("summary lacks positional label %q:\n%s", want, out.Summary)
		}
	}
	for _, a := range f.api.annotations[101] {
		if a.Title != "Job 2" && a.Title != "Job 2, step 1" || a.Message == "" {
			t.Fatalf("annotation=%+v", a)
		}
	}
}

func TestGitHubAppCheckRunStopsDetailsWhenRepositoryTurnsPublic(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	f.addReportingNode("private-job", runstate.NodeFailed, "private-error")
	f.report("private-job", "private-summary", pathAnnotations(120, "private"), 1)
	f.api.afterWrite = func(g *completionGitHub) { g.private = false }
	f.finish(runstate.RunFailed)
	f.reconcile()
	if len(f.api.checks) < 2 {
		t.Fatalf("the check run was not rewritten after the repository turned public: %d writes", len(f.api.checks))
	}
	for i, check := range f.api.checks {
		if f.api.privateAtWrite[i] {
			continue
		}
		if out := check.Output; len(out.Annotations) != 0 || strings.Contains(out.Summary+out.Text, "private-") {
			t.Fatalf("write %d to a public repository carried details: %+v", i, out)
		}
	}
	if desired, delivered, _, _ := f.revision(); delivered != desired {
		t.Fatalf("revisions=%d/%d", delivered, desired)
	}
}

func TestGitHubAppCheckRunVisibilityRecheckFailureStopsDetails(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	f.addReportingNode("private-job", runstate.NodeFailed, "private-error")
	f.report("private-job", "private-summary", pathAnnotations(120, "private"), 1)
	f.api.afterWrite = func(g *completionGitHub) { g.repoStatus = 500 }
	f.finish(runstate.RunFailed)
	f.reconcile()
	if len(f.api.checks) != 1 || len(f.api.annotations[101]) != 50 {
		t.Fatalf("writes continued after visibility became unknown: writes=%d annotations=%d", len(f.api.checks), len(f.api.annotations[101]))
	}
	if _, delivered, _, _ := f.revision(); delivered != 0 {
		t.Fatal("a partly written check run was acknowledged")
	}
}

func TestGitHubAppCheckRunPublicRetryOverwritesPrivateDetails(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.private = true
	f.addReportingNode("private-job", runstate.NodeFailed, "private-error")
	f.report("private-job", "private-summary", pathAnnotations(120, "private"), 1)
	f.finish(runstate.RunFailed)
	f.api.failWrite = 2
	f.reconcile()
	if len(f.api.annotations[101]) != 50 || !strings.Contains(f.completedCheck().Output.Text, "private-error") {
		t.Fatalf("setup: annotations=%d", len(f.api.annotations[101]))
	}
	f.api.private = false
	f.now = f.now.Add(time.Hour)
	f.reconcile()
	out := f.completedCheck().Output
	if len(out.Annotations) != 0 || strings.Contains(out.Title+out.Summary+out.Text, "private-") {
		t.Fatalf("public retry carried details: %+v", out)
	}
	if out.Text == "" {
		t.Fatal("public retry left the earlier private text in place")
	}
	// safety: GitHub has no API that removes a check run's annotations; the docs state that sent ones remain.
	if got := len(f.api.annotations[101]); got != 50 {
		t.Fatalf("annotations=%d", got)
	}
	if desired, delivered, _, _ := f.revision(); delivered != desired {
		t.Fatalf("revisions=%d/%d", delivered, desired)
	}
}

func TestGitHubAppCheckRunUnknownVisibilityNeverSendsDetails(t *testing.T) {
	for name, status := range map[string]int{"missing": 0, "unavailable": 500} {
		t.Run(name, func(t *testing.T) {
			f := newGitHubCompletion(t)
			f.api.repoStatus = status
			f.addReportingNode("private-job", runstate.NodeFailed, "private-error")
			f.report("private-job", "private-summary", pathAnnotations(1, "private"), 1)
			f.finish(runstate.RunFailed)
			f.reconcile()
			for _, check := range f.api.checks {
				if out := check.Output; len(out.Annotations) != 0 || strings.Contains(out.Summary+out.Text, "private-") {
					t.Fatalf("unknown visibility carried details: %+v", out)
				}
			}
			if status != 0 && len(f.api.checks) != 0 {
				t.Fatalf("a failed visibility read still wrote %d checks", len(f.api.checks))
			}
		})
	}
}
