package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type scheduleWriterRefusal struct {
	*store.Tenant
	failure error
}

func (f *scheduleWriterRefusal) ReplaceAuthorizedSchedules(context.Context, capability.Authorized, store.ScheduleRepoRequest) ([]string, []string, error) {
	return nil, nil, f.failure
}

type scheduleIntakePolicy struct{}

func (scheduleIntakePolicy) ResolveIntake(_ context.Context, _ capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	return store.IntakePolicyFacts{Options: &request.Request.Options, SourceLocation: &request.Request.Source.Location, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
}

func (scheduleIntakePolicy) ResolveScheduleSource(_ context.Context, row store.CronSchedule, request runspec.Request) (runspec.Request, error) {
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{RemoteURL: row.RepoPath, GitHubRepositoryID: runspec.Optional[int64]{Set: true, Value: 1}})
	request.Git = runspec.Git{Repository: repository, Ref: row.GitBranch, SHA: row.LockedRef}
	request.Source = runspec.Source{Ref: row.LockedRef, Location: runspec.SourceLocation{RemoteURL: row.RepoPath, PipelineDir: ".sparkwing"}}
	return request, err
}

type scheduleHTTPFixture struct {
	s      *serverFixture
	writer *scheduleWriterRefusal
	raw    string
	facts  *reportingFacts
}

func newScheduleHTTPFixture(t *testing.T) scheduleHTTPFixture {
	t.Helper()
	return newDeploymentScheduleHTTPFixture(t, deployment.Config{})
}

func newDeploymentScheduleHTTPFixture(t *testing.T, cfg deployment.Config) scheduleHTTPFixture {
	t.Helper()
	s := &serverFixture{}
	newStoreForAuth(t, s.option(cfg, nil), store.WithPolicies(func(*store.Store) (store.Policies, error) { return store.Policies{Intake: scheduleIntakePolicy{}}, nil }))
	tenant, err := s.store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	writer := &scheduleWriterRefusal{Tenant: tenant}
	raw, issued, err := teststore.ProvisionCredential(t.Context(), s.store, capability.ProvisionSpec{
		Kind: capability.ProvisionMember, Team: string(tenant.Team()), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "pusher"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.RunsControl},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.store.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: issued.Digest, Audience: issued.Audience}, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	facts := &reportingFacts{digest: issued.Digest, snapshot: snapshot}
	s.WithAuthorization(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(facts), Bindings: facts})
	return scheduleHTTPFixture{s, writer, raw, facts}
}

func (f scheduleHTTPFixture) call(t *testing.T, method, path, body, key string, want int, out any) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.raw)
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: HTTP %d %s, want %d; fake error=%v", method, path, w.Code, w.Body, want, f.writer.failure)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

const (
	schedulePushJSON = `{"repo_url":"https://github.com/acme/widgets.git","branch":"main","sha":"0123456789abcdef0123456789abcdef01234567","schedules":[{"pipeline":"nightly","cron":"0 3 * * *","tz":"UTC","args":{"depth":"deep"}},{"pipeline":"sweep","name":"quick","cron":"*/5 * * * *"}]}`
	scheduleHTTPPath = "/api/v1/schedules/acme%2Fwidgets%2Fnightly"
)

func TestScheduleHTTPReadWriteAndReplay(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	var pushed controllerwire.ScheduleRepoWritten
	f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 200, &pushed)
	if len(pushed.Schedules) != 2 {
		t.Fatalf("push=%+v", pushed)
	}
	stored, err := f.writer.GetCronSchedule(t.Context(), pushed.Schedules[0].ID)
	if err != nil || stored.Team != store.DefaultTeam || stored.ArmedBy != "pusher" {
		t.Fatalf("stored=%+v error=%v", stored, err)
	}
	var list controllerwire.ScheduleOverview
	f.call(t, "GET", "/api/v1/schedules?next=2&team=foreign", "", "", 200, &list)
	if len(list.Schedules) != 2 || len(list.Upcoming) != 4 || !list.Health.TickStale || list.Health.Armed != 2 {
		t.Fatalf("list=%+v", list)
	}
	var detail controllerwire.DetailView
	f.call(t, "GET", scheduleHTTPPath+"?fires=0&next=2", "", "", 200, &detail)
	if len(detail.Upcoming) != 2 || detail.Fires == nil || len(detail.Fires) != 0 || detail.Schedule.Effective.Args.Values()["depth"] != "***" {
		t.Fatalf("detail=%+v", detail)
	}
	for _, action := range []string{"pause", "resume"} {
		var out controllerwire.ScheduleEnvelope
		f.call(t, "POST", scheduleHTTPPath+"/"+action, "", "", 200, &out)
		if out.Schedule.Paused != (action == "pause") {
			t.Fatalf("%s=%+v", action, out)
		}
	}
	var first, replay controllerwire.ScheduleRun
	f.call(t, "POST", scheduleHTTPPath+"/run", "", "same", 200, &first)
	f.call(t, "POST", scheduleHTTPPath+"/run", "", "same", 200, &replay)
	runs, err := f.writer.ListRuns(t.Context(), store.RunFilter{})
	if err != nil || first.Replayed || !replay.Replayed || first.RunID != replay.RunID || first.Schedule.ID != replay.Schedule.ID || len(runs) != 1 {
		t.Fatalf("first=%+v replay=%+v", first, replay)
	}
	f.call(t, "POST", scheduleHTTPPath+"/run", "", "", 400, nil)
	f.call(t, "GET", "/api/v1/schedules/crn_missing", "", "", 404, nil)
	f.call(t, "GET", "/api/v1/schedules/missing", "", "", 404, nil)
}

func TestScheduleHTTPLocalHealthDoesNotInventControllerLoop(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	home := t.TempDir()
	f.s.WithLocalSchedules(&crons.Service{
		Operator: f.s.store.AsOperator(), Schedules: f.writer.Tenant, Side: store.CronWhereLocal,
		NativeTimer: &crontimer.Host{GOOS: "linux", Home: home, ConfigHome: filepath.Join(home, ".config"), SparkwingHome: filepath.Join(home, ".sparkwing")},
	}, nil, nil)
	var overview controllerwire.ScheduleOverview
	f.call(t, "GET", "/api/v1/schedules", "", "", 200, &overview)
	if overview.Health.Timer.Installed || overview.Health.Timer.Enabled || overview.Health.Timer.Detail == "controller loop" {
		t.Fatalf("local health invents an evaluator loop: %+v", overview.Health)
	}
	if !strings.Contains(overview.Health.Detail, "no native schedule timer is installed") {
		t.Fatalf("missing local timer remedy: %+v", overview.Health)
	}
}

func TestScheduleHTTPLocalHealthKeepsRowsWhenNativeStateIsUnknown(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f := newScheduleHTTPFixture(t)
			f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 200, nil)
			home := t.TempDir()
			host := &crontimer.Host{GOOS: goos, Home: home, ConfigHome: filepath.Join(home, ".config"), SparkwingHome: filepath.Join(home, ".sparkwing"), Binary: filepath.Join(home, "sparkwing"), Version: "test"}
			if goos == "linux" {
				host.Exec = func(context.Context, string, ...string) (string, error) { return "", nil }
				if _, err := crontimer.Install(t.Context(), *host); err != nil {
					t.Fatal(err)
				}
				host.Exec = func(context.Context, string, ...string) (string, error) {
					return "", errors.New("service manager unavailable")
				}
			}
			f.s.WithLocalSchedules(&crons.Service{
				Operator: f.s.store.AsOperator(), Schedules: f.writer.Tenant, Side: store.CronWhereLocal, NativeTimer: host,
			}, nil, nil)
			var overview controllerwire.ScheduleOverview
			f.call(t, "GET", "/api/v1/schedules", "", "", 200, &overview)
			if len(overview.Schedules) != 2 || overview.Health.Timer.Enabled || !strings.Contains(overview.Health.Detail, "state is unavailable") {
				t.Fatalf("unknown timer hides rows or claims enabled: %+v", overview)
			}
		})
	}
}

type observedCronContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedCronContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestScheduleHTTPHealthFollowsControllerLoopLifetime(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := &observedCronContext{Context: ctx, entered: make(chan struct{})}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		f.s.runCronTick(observed, time.Hour)
	}()
	<-observed.entered
	for _, running := range []bool{true, false} {
		if !running {
			cancel()
			<-stopped
		}
		var overview controllerwire.ScheduleOverview
		f.call(t, "GET", "/api/v1/schedules", "", "", 200, &overview)
		if overview.Health.Timer.Enabled != running || overview.Health.Timer.Installed != running {
			t.Fatalf("loop running=%v, health=%+v", running, overview.Health)
		}
	}
}

func TestScheduleHTTPOverrideAndWithdrawal(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 200, nil)
	var out controllerwire.ScheduleEnvelope
	f.call(t, "PUT", scheduleHTTPPath+"/override", `{"cron":"*/15 * * * *","tz":"America/Denver","overlap":"queue","catch_up":"0s","args":{}}`, "", 200, &out)
	if out.Schedule.Effective.Cron != "*/15 * * * *" || out.Schedule.Effective.TZ != "America/Denver" || out.Schedule.Effective.Overlap != "queue" || out.Schedule.Effective.CatchUpNS != 0 || len(out.Schedule.Effective.Args.Values()) != 0 || len(out.Schedule.Override.Fields) != 5 {
		t.Fatalf("override=%+v", out)
	}
	f.call(t, "DELETE", scheduleHTTPPath+"/override", "", "", 200, &out)
	if out.Schedule.Effective.Cron != "0 3 * * *" || out.Schedule.Effective.Args.Values()["depth"] != "***" || len(out.Schedule.Override.Fields) != 0 {
		t.Fatalf("reset=%+v", out)
	}
	var withdrawn controllerwire.ScheduleRepoWritten
	f.call(t, "PUT", "/api/v1/schedules/repos", `{"repo_url":"https://github.com/acme/widgets.git"}`, "", 200, &withdrawn)
	if len(withdrawn.Withdrawn) != 2 {
		t.Fatalf("withdrawal=%+v", withdrawn)
	}
	var list controllerwire.ScheduleOverview
	f.call(t, "GET", "/api/v1/schedules", "", "", 200, &list)
	if len(list.Schedules) != 0 {
		t.Fatalf("default list=%+v", list)
	}
	f.call(t, "GET", "/api/v1/schedules?all=true", "", "", 200, &list)
	if len(list.Schedules) != 2 {
		t.Fatalf("complete list=%+v", list)
	}
	var deleted controllerwire.ScheduleDeletion
	f.call(t, "DELETE", scheduleHTTPPath, "", "", 200, &deleted)
	if !deleted.Deleted || deleted.ScheduleID == "" {
		t.Fatalf("delete=%+v", deleted)
	}
	var removed controllerwire.ScheduleRepoDeletion
	f.call(t, "DELETE", "/api/v1/schedules/repos?repo_url=https://github.com/acme/widgets.git", "", "", 200, &removed)
	if removed.Removed != 1 {
		t.Fatalf("repo delete=%+v", removed)
	}
}

func TestScheduleHTTPInvalidInputAndBackend(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	for _, body := range []string{
		`{"repo_url":"/home/me/widgets"}`, `{"repo_url":""}`,
		`{"repo_url":"https://github.com/acme/widgets.git","branch":"has a space"}`,
		`{"repo_url":"https://github.com/acme/widgets.git","branch":"feature/../main"}`,
		`{"repo_url":"https://github.com/acme/widgets.git","schedules":[{"pipeline":"nightly","cron":"nope"}]}`,
		`{"repo_url":"https://github.com/acme/widgets.git","schedules":[{"cron":"0 3 * * *"}]}`,
	} {
		f.call(t, "PUT", "/api/v1/schedules/repos", body, "", 400, nil)
	}
	for _, body := range []string{`{}`, `{"catch_up":"soon"}`, `{"catch_up":"-1s"}`, `{"args":null}`, `{"cron":"nope"}`} {
		f.call(t, "PUT", scheduleHTTPPath+"/override", body, "", 400, nil)
	}
	for _, query := range []string{"next=-1", "next=invalid", "next=1&next=2", "all=perhaps"} {
		f.call(t, "GET", "/api/v1/schedules?"+query, "", "", 400, nil)
	}
	for _, branch := range []string{"has a space", "feature/../main", "-leading-dash", "trailing/", "tip.lock", "ref@{0}", "double//slash", "star*"} {
		body := strings.Replace(schedulePushJSON, `"main"`, strconv.Quote(branch), 1)
		f.call(t, "PUT", "/api/v1/schedules/repos", body, "", 400, nil)
	}
	f.writer.failure = &apierr.Error{Code: apierr.Forbidden, Message: "authority changed"}
	testOnlyTenantPort[scheduleWriter](f.s, func(context.Context, store.Team) (scheduleWriter, error) { return f.writer, nil })
	f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 403, nil)
	rows, err := f.writer.ListCronSchedules(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed transaction leaked schedules: %+v %v", rows, err)
	}
	testOnlyTenantPort[scheduleWriter](f.s, func(context.Context, store.Team) (scheduleWriter, error) { return nil, nil })
	w := f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 500, nil)
	if !strings.Contains(w.Body.String(), "policy_missing") {
		t.Fatal(w.Body.String())
	}
}

func TestScheduleHTTPInvalidCronNamesFieldAndExample(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	w := f.call(t, "PUT", scheduleHTTPPath+"/override", `{"cron":"61 * * * *"}`, "", 400, nil)
	for _, want := range []string{"bad_request", "cron", "minute field", "0-59", "0 * * * *"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("response = %s, want %q", w.Body, want)
		}
	}
}

func TestScheduleHTTPRetainedHistoryAndCounts(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	var pushed controllerwire.ScheduleRepoWritten
	f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 200, &pushed)
	id := pushed.Schedules[0].ID
	base := time.Now().UTC().Add(-100 * time.Hour)
	for i := range 60 {
		at := base.Add(time.Duration(i) * time.Hour)
		fire := &store.CronFire{DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeMissed, Detail: "seeded"}
		if err := f.writer.ResolveCronDue(t.Context(), id, at, nil, fire); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []struct {
		text  string
		fires int
		next  int
	}{{"", 60, 5}, {"?fires=3&next=2", 3, 2}, {"?fires=0&next=0", 0, 0}} {
		var detail controllerwire.DetailView
		f.call(t, "GET", "/api/v1/schedules/"+id+query.text, "", "", 200, &detail)
		if detail.Fires == nil || len(detail.Fires) != query.fires || len(detail.Upcoming) != query.next {
			t.Fatalf("%s: fires=%d next=%d", query.text, len(detail.Fires), len(detail.Upcoming))
		}
	}
}
