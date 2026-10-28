package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

var updateConsoleFixtures = flag.Bool("update-console-fixtures", false, "rewrite the console's wire fixtures")

var consoleFixtureStart = time.Date(2026, 10, 28, 5, 1, 19, 0, time.UTC)

const consoleFixtureSHA = "1b8e292f29420af1ae8e113e53faf3c05fb17249"

type consoleClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *consoleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *consoleClock) tick() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(3 * time.Second)
	return c.now
}

type consoleRun struct {
	e       *engineFixture
	tenant  *store.Tenant
	clock   *consoleClock
	handler http.Handler
	reader  string
	bearers map[string]string
}

func consoleRunFixture(t *testing.T) *consoleRun {
	t.Helper()
	clock := &consoleClock{now: consoleFixtureStart}
	e, _ := engineRuntimeStore(t, teststore.WithClock(clock.Now))
	e.claimProfiles = engineProfileFake{}
	e.VersionPolicy = engineVersionPolicy(&engineVersionFake{notices: []engineid.Notice{{Kind: engineid.NoticeSupportEnding, Version: "v1.2.3", Reason: "v1.2.3 leaves support on 2027-01-31; upgrade the engine"}}})
	c := &consoleRun{e: e, clock: clock}
	engineSQL(t, e.Store, `INSERT INTO teams(name,created_at,updated_at,next_run_number) VALUES ('acme',1,1,16)`)
	tenant, err := e.Store.ForTeam(t.Context(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	c.tenant = tenant
	engineHTTPRoot(t, e, "acme", "moonborn", []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
	c.reader, _, err = teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "acme", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "reader"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	c.handler = e.Server.WithEngineController(e.EngineController).Handler()
	c.seedRun(t, "run-plan", 14, "")
	c.seedRun(t, "run-jobs", 15, "run-earlier")

	owner := c.claim(t, capability.UnitTrigger, "run-plan", "")
	c.clock.tick()
	if _, err := e.Finish(t.Context(), owner, controllerwire.FinishRequest{Error: &apierr.Error{Code: apierr.PlanFailed, Message: "install source entry: # pipeline\njobs/broken.go:6:9: undefined: qaDeliberateUndefined"}}); err != nil {
		t.Fatal(err)
	}

	c.seedPlan(t)
	c.execute(t, "lint", nil, controllerwire.JobResult{Outcome: "success"})
	c.execute(t, "build", nil, controllerwire.JobResult{Outcome: "failed", MaskedError: "install source entry: ./pipeline.go:3:9: cannot use *** as int value"})
	c.execute(t, "notify", nil, controllerwire.JobResult{Outcome: "success"})
	c.execute(t, "test", c.retriedTest, controllerwire.JobResult{Outcome: "success"})
	// hack: only retry intake writes a retry selection, and it needs a whole source run to read from.
	engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='acme' AND id='run-jobs'`, []byte(`{"retry_of":"run-earlier","retry":{"mode":"failed","results":{"lint":{"Ref":{"run_id":"run-earlier","node_id":"lint"},"StateSeq":3}}},"admission_key":"capability:fixture","admission_subject":{"kind":"account","id":"account"}}`))
	return c
}

func (c *consoleRun) seedRun(t *testing.T, id string, number int64, retryOf string) {
	t.Helper()
	// hack: runs are seeded by SQL because intake needs a checkout and admission to accept a request.
	request := runspec.SealedRequest{Version: 1, RunID: id, Hash: "request", Request: runspec.Request{
		Version: 1, Team: "acme", Pipeline: "hello", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost,
		RequestedArgs: map[string]string{"target": "prod"}, Lineage: runspec.Lineage{RetryOf: retryOf},
		Git:    runspec.Git{Repository: runspec.Repository{Key: "git:gitlab.com/acme/app", Display: "gitlab.com/acme/app"}, Ref: "main", SHA: consoleFixtureSHA},
		Source: runspec.Source{Ref: consoleFixtureSHA},
	}}
	raw, err := json.Marshal(withProvenance(request, assertedProvenance(t, "hello", "main", consoleFixtureSHA)))
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := json.Marshal(map[string]any{"retry_of": retryOf, "admission_key": "capability:fixture", "admission_subject": map[string]string{"kind": "account", "id": "account"}})
	if err != nil {
		t.Fatal(err)
	}
	now := c.clock.Now().UnixNano()
	engineSQL(t, c.e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,started_at,lineage_json,log_retention_class,log_retention_days) VALUES ('acme',?,?,'hello','running',?,'request',1,'git:gitlab.com/acme/app','host','execute','account',?,?,?,'r30',30)`, id, number, raw, now, now, lineage)
	engineSQL(t, c.e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('acme',?,'trigger',1,1,?,?,?)`, id, c.clock.Now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
}

func (c *consoleRun) seedPlan(t *testing.T) {
	t.Helper()
	// hack: the plan and job declarations are seeded by SQL because plan acceptance needs a planning runner.
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "lint"},
		{ID: "build", Needs: []string{"lint"}},
		{ID: "test", Needs: []string{"lint"}, Groups: []string{"checks"}, Retry: &nodeproto.RetrySpec{Attempts: 1}},
		{ID: "notify", OnFailureOf: "build"},
		{ID: "deploy", Needs: []string{"test"}, Groups: []string{"release"}, Approval: &nodeproto.ApprovalSpec{}},
	}}
	retry := runspec.JobExecutionSettings{}
	retry.Retry.Set, retry.Retry.Value = true, 1
	execution, err := json.Marshal(retry)
	if err != nil {
		t.Fatal(err)
	}
	for seq, job := range plan.Jobs {
		spec, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		settings, ready := []byte(`{}`), any(c.clock.Now().UnixNano())
		if job.ID == "test" {
			settings = execution
		}
		if job.ID == "deploy" {
			ready = nil
		}
		engineSQL(t, c.e.Store, `INSERT INTO nodes(team,run_id,node_id,seq,status,ready_at,spec_json,execution_json) VALUES ('acme','run-jobs',?,?,'pending',?,?,?)`, job.ID, seq, ready, spec, settings)
	}
	engineAcceptedFixturePlanDoc(t, c.e, "acme", "run-jobs", plan)
}

func (c *consoleRun) claim(t *testing.T, kind capability.UnitKind, run, job string) capability.Authorized {
	t.Helper()
	award, err := c.e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "moonborn", Target: &store.UnitRef{Kind: kind, RunID: runspec.RunID(run), NodeID: runspec.NodeID(job)}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := c.e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(award.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	if c.bearers == nil {
		c.bearers = map[string]string{}
	}
	c.bearers[run+"/"+job] = string(award.Bearer)
	return capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "acme", RunID: run, NodeID: job}}
}

func (c *consoleRun) execute(t *testing.T, job string, work func(*testing.T, capability.Authorized, controllerwire.Applied) controllerwire.Applied, result controllerwire.JobResult) {
	t.Helper()
	a := c.claim(t, capability.UnitNode, "run-jobs", job)
	engineAdmitExecution(t, c.e.Store, a.Principal)
	c.clock.tick()
	started, err := c.e.TransitionJob(t.Context(), a, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: new(int64(0))})
	if err != nil {
		t.Fatal(err)
	}
	if work != nil {
		started = work(t, a, started)
	}
	c.clock.tick()
	to := result.Outcome
	if _, err := c.e.TransitionJob(t.Context(), a, controllerwire.JobTransitionRequest{To: to, Cause: "report", SourceAttempt: &started.Attempt, Result: &result}); err != nil {
		t.Fatal(err)
	}
}

func (c *consoleRun) step(t *testing.T, a capability.Authorized, attempt controllerwire.Applied, id, state, masked string) {
	t.Helper()
	at := c.clock.tick()
	report := store.StepReport{
		Attempt: runspec.AttemptIdentity{Team: "acme", RunID: "run-jobs", NodeID: a.Target.NodeID, Attempt: attempt.Attempt, Generation: attempt.Generation},
		Actor:   engineActor(a), StepID: id, State: state, MaskedError: masked, StartedAt: &at,
	}
	if state != "running" {
		report.StartedAt, report.FinishedAt = nil, &at
	}
	if err := c.tenant.RecordStep(t.Context(), report); err != nil {
		t.Fatal(err)
	}
}

func (c *consoleRun) annotate(t *testing.T, a capability.Authorized, step string, annotation nodeproto.Annotation) {
	t.Helper()
	body, err := json.Marshal(controllerwire.AnnotationWrite{Annotation: annotation, StepID: step})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/run-jobs/jobs/" + a.Target.NodeID + "/annotations"
	if step != "" {
		path = "/api/v1/runs/run-jobs/jobs/" + a.Target.NodeID + "/steps/annotations"
	}
	// safety: fixtures annotate through the reporting route a host uses, so its level and range checks apply.
	engineClaimHTTP(t, c.handler, c.bearers["run-jobs/"+a.Target.NodeID], http.MethodPost, path, string(body), http.StatusNoContent)
}

func (c *consoleRun) summarize(t *testing.T, a capability.Authorized, step, markdown string) {
	t.Helper()
	ref := runspec.NodeRef{RunID: "run-jobs", NodeID: runspec.NodeID(a.Target.NodeID)}
	if err := c.tenant.WriteSummary(t.Context(), store.SummaryWrite{Ref: ref, Actor: engineActor(a), StepID: step, Markdown: markdown}); err != nil {
		t.Fatal(err)
	}
}

func (c *consoleRun) retriedTest(t *testing.T, a capability.Authorized, first controllerwire.Applied) controllerwire.Applied {
	c.step(t, a, first, "unit", "running", "")
	c.annotate(t, a, "unit", nodeproto.Annotation{Level: "error", Message: "TestCheckout timed out", File: "checkout_test.go", Line: 41})
	c.summarize(t, a, "unit", "## Attempt 1\n41 passed, 1 failed")
	c.step(t, a, first, "unit", "failed", "exit status 1")
	c.clock.tick()
	second, err := c.e.TransitionJob(t.Context(), a, controllerwire.JobTransitionRequest{To: "running", Cause: "retry", SourceAttempt: &first.Attempt, Result: &controllerwire.JobResult{Outcome: "failed", Reason: "step_failed"}})
	if err != nil {
		t.Fatal(err)
	}
	c.step(t, a, second, "setup", "running", "")
	c.step(t, a, second, "setup", "passed", "")
	c.step(t, a, second, "unit", "running", "")
	c.annotate(t, a, "unit", nodeproto.Annotation{Level: "info", Message: "42 tests", File: "checkout_test.go", Line: 7})
	c.summarize(t, a, "unit", "## Coverage\n91%")
	c.step(t, a, second, "unit", "passed", "")
	c.annotate(t, a, "", nodeproto.Annotation{Level: "warn", Message: "coverage fell 2%", File: "coverage.out", Line: 1, EndLine: 3, Column: 2})
	c.summarize(t, a, "", "## Test report\n42 passed on the second attempt")
	return second
}

func (c *consoleRun) get(t *testing.T, path string) []byte {
	t.Helper()
	w := engineClaimHTTP(t, c.handler, c.reader, http.MethodGet, path, "", http.StatusOK)
	return w.Body.Bytes()
}

func ptr[T any](v T) *T { return &v }

func handBuiltConsoleFixtures() map[string]any {
	// hack: these marshal controllerwire values rather than handler responses, so they pin field names only.
	started := consoleFixtureStart
	finished := started.Add(33 * time.Second)
	inputs := runspec.Display{
		Pipeline: "hello", RequestedArgs: map[string]string{}, Args: map[string]string{"target": "prod"}, SecretArgs: []string{},
		Git:     runspec.Git{Repository: runspec.Repository{Key: "git:gitlab.com/acme/app", Display: "gitlab.com/acme/app"}, Ref: "main", SHA: consoleFixtureSHA},
		Lineage: runspec.Lineage{RetryOf: "run-earlier"},
	}
	summary := runstate.RunSummary{
		ID: "run-jobs", Number: 15, Pipeline: "hello", Status: "failed", TriggerSource: "github", GitBranch: "main",
		GitSHA: consoleFixtureSHA, Args: map[string]string{"target": "prod"}, Error: "build failed",
		CreatedAt: started, StartedAt: started, FinishedAt: &finished, DeclaredRepo: "acme/app", RepoURL: "https://github.com/acme/app",
		GithubOwner: "acme", GithubRepo: "app", RetryOf: "run-earlier", TriggerUser: "octocat",
	}
	schedule := controllerwire.ScheduleView{
		ID: "sched-nightly", Name: "app/nightly", ScheduleName: "", RepoPath: "/src/app", Pipeline: "nightly", Where: "controller",
		Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip", Args: controllerwire.DisplayScheduleArgs(map[string]string{"target": "prod"}),
		Lock: controllerwire.LockView{State: "follows"}, Override: controllerwire.OverrideView{Fields: []string{}, Args: controllerwire.DisplayScheduleArgs(nil)},
		Effective: controllerwire.EffectiveView{Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip", Args: controllerwire.DisplayScheduleArgs(map[string]string{"target": "prod"})},
		Declared:  true, State: "armed", ArmedAt: "2026-10-27T03:00:00Z", UpdatedAt: "2026-10-27T03:00:00Z",
		LastFiredAt: ptr("2026-10-28T03:00:00Z"), LastRunID: "run-jobs", LastOutcome: "fired", NextDueAt: ptr("2026-10-29T03:00:00Z"),
	}
	return map[string]any{
		"runs-list.json":   map[string]any{"runs": []runstate.RunSummary{summary}},
		"run-created.json": controllerwire.CreateRunResponse{RunID: "run-retry", Number: 16, Inputs: inputs, WriteScope: "team"},
		"runners.json": controllerwire.Runners{Runners: []controllerwire.RunnerDetail{{
			Name: "moonborn", Class: "runner", Location: "local", Labels: []string{"os=linux", "arch=amd64"}, ReservedPools: []string{}, Repos: []string{},
			LastSeen: "2026-10-28T05:01:19Z", Status: "busy", ActiveJobs: []controllerwire.RunnerJob{{RunID: "run-jobs", JobID: "test"}}, MaxConcurrent: 2,
			Capacity: controllerwire.RunnerResources{Cores: 8, MemoryBytes: 16 << 30},
			Headroom: &controllerwire.RunnerHeadroom{RunnerResources: controllerwire.RunnerResources{Cores: 6, MemoryBytes: 12 << 30}, QueueDepth: 0, ObservedAt: "2026-10-28T05:01:19Z"},
		}}},
		"pipelines.json": pipelinesList{Pipelines: []controllerwire.PipelineSummary{{Name: "hello", LastActivityAt: finished, LastRunID: "run-jobs", LastStatus: "failed", LastRunAt: &started, LastFinishedAt: &finished}}},
		"schedules.json": controllerwire.ScheduleOverview{
			Health:    controllerwire.HealthView{Schedules: 1, Armed: 1, Following: 1},
			Schedules: []controllerwire.ScheduleView{schedule},
			Upcoming:  []controllerwire.ScheduleUpcoming{{Schedule: "sched-nightly", Name: "app/nightly", At: "2026-10-29T03:00:00Z"}},
		},
		"schedule-detail.json": controllerwire.DetailView{
			Schedule: schedule,
			Fires: []controllerwire.FireView{{
				ID: "fire-1", ScheduleID: "sched-nightly", DueAt: "2026-10-28T03:00:00Z", DecidedAt: "2026-10-28T03:00:01Z", Outcome: "fired",
				RunID: "run-jobs", Args: controllerwire.DisplayScheduleArgs(map[string]string{"target": "prod"}), RunStatus: "failed",
			}},
			Upcoming: []string{"2026-10-29T03:00:00Z"},
		},
		"schedule-run.json": controllerwire.ScheduleRun{RunID: "run-scheduled", Schedule: schedule},
		"github-runners.json": githubBindingsResp{
			Bindings: []githubBindingJSON{{Repository: "acme/app", RepositoryID: 9, RepositoryOwnerID: 8, CreatedBy: "account:owner", CreatedAt: 1793250079}},
			Workflow: "name: sparkwing\n# --team acme\n",
		},
		"git-credential-releases.json": map[string]any{"releases": []gitCredentialReleaseJSON{{
			Host: "gitlab.com", Repository: "git:gitlab.com/acme/app", RunID: "run-jobs", JobID: "build", Generation: 1,
			Runner: "moonborn", CapabilityPrefix: "swk_abc123", ReleasedAt: 1793250079,
		}}},
	}
}

// TestConsoleFixturesMatchTheWireTypes keeps web/src/lib/testdata in step with the controller. The run
// detail and step fixtures are the controller's own responses to a reader for runs persisted through the
// store's write paths; the rest are marshaled controllerwire values. The console's unit tests run its
// adapters over those files and its browser mocks serve them, so a changed response fails here until the
// fixtures are regenerated with -update-console-fixtures and the console follows.
func TestConsoleFixturesMatchTheWireTypes(t *testing.T) {
	fixtures := map[string][]byte{}
	run := consoleRunFixture(t)
	for name, path := range map[string]string{
		"run-detail-plan-failure.json": "/api/v1/runs/run-plan",
		"run-detail-jobs.json":         "/api/v1/runs/run-jobs",
		"run-steps.json":               "/api/v1/runs/run-jobs/steps",
	} {
		var body bytes.Buffer
		if err := json.Indent(&body, run.get(t, path), "", "  "); err != nil {
			t.Fatal(err)
		}
		fixtures[name] = append(bytes.TrimSpace(body.Bytes()), '\n')
	}
	for name, value := range handBuiltConsoleFixtures() {
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		fixtures[name] = append(body, '\n')
	}
	kinds, err := json.MarshalIndent(runstate.RunEventKinds, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fixtures["../runEventKinds.json"] = append(kinds, '\n')
	for name, want := range fixtures {
		path := filepath.Join("..", "..", "web", "src", "lib", "testdata", name)
		if *updateConsoleFixtures {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from what the controller produces; rerun with -update-console-fixtures and update the console", name)
		}
	}
}
