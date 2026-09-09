package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const cronTestRepoURL = "https://github.com/acme/widgets.git"

const cronTestSHA = "0123456789abcdef0123456789abcdef01234567"

type cronsFixture struct {
	t      *testing.T
	url    string
	store  *store.Store
	writer string
	reader string
	none   string
}

func newCronsFixture(t *testing.T) *cronsFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	writer, _, err := st.CreateToken("pusher", store.TokenKindUser,
		[]string{controller.ScopeRunsRead, controller.ScopeRunsWrite}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken writer: %v", err)
	}
	reader, _, err := st.CreateToken("viewer", store.TokenKindUser,
		[]string{controller.ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken reader: %v", err)
	}
	none, _, err := st.CreateToken("stranger", store.TokenKindUser,
		[]string{controller.ScopeTriggersRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return &cronsFixture{t: t, url: srv.URL, store: st, writer: writer, reader: reader, none: none}
}

// safety: the request and its body's close live in one function, so no caller
// can leak a response by handing it on.
func (f *cronsFixture) call(method, path, token string, body any, want int, out any) int {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.url+path, reader)
	if err != nil {
		f.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if want == 0 {
		return resp.StatusCode
	}
	if resp.StatusCode != want {
		read, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("%s %s: status = %d, want %d: %s", method, path, resp.StatusCode, want, bytes.TrimSpace(read))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			f.t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode
}

// safety: a zero want means the caller judges the status itself, so nothing is
// asserted here beyond the request landing.
func (f *cronsFixture) status(method, path, token string, body any) int {
	f.t.Helper()
	return f.call(method, path, token, body, 0, nil)
}

func (f *cronsFixture) push(body map[string]any) map[string]any {
	f.t.Helper()
	var out map[string]any
	f.call(http.MethodPut, "/api/v1/crons/repos", f.writer, body, http.StatusOK, &out)
	return out
}

func cronPushBody() map[string]any {
	return map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "nightly", "cron": "0 3 * * *", "tz": "UTC", "args": map[string]string{"depth": "deep"}},
			{"pipeline": "sweep", "name": "quick", "cron": "*/5 * * * *"},
		},
	}
}

func TestControllerCrons_PushStoresRowsAndListsThem(t *testing.T) {
	f := newCronsFixture(t)
	pushed := f.push(cronPushBody())
	schedules, ok := pushed["schedules"].([]any)
	if !ok || len(schedules) != 2 {
		t.Fatalf("push returned %v, want two schedules", pushed["schedules"])
	}

	var overview crons.OverviewView
	f.call(http.MethodGet, "/api/v1/crons", f.reader, nil, http.StatusOK, &overview)
	if len(overview.Schedules) != 2 {
		t.Fatalf("overview holds %d schedules, want two", len(overview.Schedules))
	}
	if overview.Health.Armed != 2 {
		t.Errorf("health armed = %d, want 2", overview.Health.Armed)
	}
	if overview.Health.Timer.Detail != crons.ControllerTimerDetail {
		t.Errorf("timer detail = %q, want %q", overview.Health.Timer.Detail, crons.ControllerTimerDetail)
	}
	byName := map[string]crons.ScheduleView{}
	for _, view := range overview.Schedules {
		byName[view.Name] = view
	}
	nightly, found := byName["acme/widgets/nightly"]
	if !found {
		t.Fatalf("no row named acme/widgets/nightly: %v", byName)
	}
	if nightly.Where != store.CronWhereController {
		t.Errorf("where = %q, want controller", nightly.Where)
	}
	if nightly.GitBranch != "main" || nightly.Lock.Ref != cronTestSHA {
		t.Errorf("branch = %q ref = %q, want main at the pushed commit", nightly.GitBranch, nightly.Lock.Ref)
	}
	if nightly.Effective.Args["depth"] != "deep" {
		t.Errorf("args = %v, want the pushed set", nightly.Effective.Args)
	}
	if _, found := byName["acme/widgets/sweep/quick"]; !found {
		t.Errorf("no row named acme/widgets/sweep/quick: %v", byName)
	}
}

func TestControllerCrons_PushWithdrawsWhatItNoLongerCarries(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	body := cronPushBody()
	body["schedules"] = []map[string]any{{"pipeline": "nightly", "cron": "0 3 * * *"}}
	second := f.push(body)
	withdrawn, _ := second["withdrawn"].([]any)
	if len(withdrawn) != 1 || withdrawn[0] != "acme/widgets/sweep/quick" {
		t.Errorf("withdrawn = %v, want the entry the push dropped", second["withdrawn"])
	}
}

func TestControllerCrons_PushRefusesABadRepositoryOrCadence(t *testing.T) {
	f := newCronsFixture(t)
	for name, body := range map[string]map[string]any{
		"no url":   {"repo_url": "", "schedules": []map[string]any{}},
		"a path":   {"repo_url": "/home/me/widgets", "schedules": []map[string]any{}},
		"bad cron": {"repo_url": cronTestRepoURL, "schedules": []map[string]any{{"pipeline": "p", "cron": "nope"}}},
		"no name":  {"repo_url": cronTestRepoURL, "schedules": []map[string]any{{"cron": "0 3 * * *"}}},
	} {
		f.call(http.MethodPut, "/api/v1/crons/repos", f.writer, body, http.StatusBadRequest, nil)
		if t.Failed() {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestControllerCrons_DetailCarriesFiresAndUpcoming(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	var detail crons.DetailView
	f.call(http.MethodGet, "/api/v1/crons/acme%2Fwidgets%2Fnightly", f.reader, nil, http.StatusOK, &detail)
	if detail.Schedule.Pipeline != "nightly" {
		t.Fatalf("schedule = %+v", detail.Schedule)
	}
	if len(detail.Upcoming) == 0 {
		t.Error("detail names no upcoming instants")
	}
	if detail.Fires == nil {
		t.Error("fires should be an empty list, not null")
	}
	f.call(http.MethodGet, "/api/v1/crons/crn_nosuchrow", f.reader, nil, http.StatusNotFound, nil)
}

func TestControllerCrons_PauseResumeAndOverride(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())
	const path = "/api/v1/crons/acme%2Fwidgets%2Fnightly"

	var env crons.ScheduleEnvelope
	f.call(http.MethodPost, path+"/pause", f.writer, nil, http.StatusOK, &env)
	if env.Schedule.State != crons.StatePaused {
		t.Errorf("state = %q, want paused", env.Schedule.State)
	}
	f.call(http.MethodPost, path+"/resume", f.writer, nil, http.StatusOK, &env)
	if env.Schedule.State != crons.StateArmed {
		t.Errorf("state = %q, want armed", env.Schedule.State)
	}

	f.call(http.MethodPut, path+"/override", f.writer,
		map[string]any{"cron": "0 5 * * *", "catch_up": "2h"}, http.StatusOK, &env)
	if env.Schedule.Effective.Cron != "0 5 * * *" {
		t.Errorf("effective cron = %q, want the override", env.Schedule.Effective.Cron)
	}
	if env.Schedule.Effective.CatchUpNS != int64(2*time.Hour) {
		t.Errorf("effective catch up = %d, want 2h", env.Schedule.Effective.CatchUpNS)
	}
	if len(env.Schedule.Override.Fields) != 2 {
		t.Errorf("override fields = %v, want cron and catch_up", env.Schedule.Override.Fields)
	}

	f.call(http.MethodDelete, path+"/override", f.writer, nil, http.StatusOK, &env)
	if env.Schedule.Effective.Cron != "0 3 * * *" {
		t.Errorf("effective cron = %q, want the declaration back", env.Schedule.Effective.Cron)
	}
	if len(env.Schedule.Override.Fields) != 0 {
		t.Errorf("override fields = %v, want none", env.Schedule.Override.Fields)
	}

	f.call(http.MethodPut, path+"/override", f.writer, map[string]any{}, http.StatusBadRequest, nil)
	f.call(http.MethodPut, path+"/override", f.writer,
		map[string]any{"catch_up": "soon"}, http.StatusBadRequest, nil)
}

func TestControllerCrons_RunNowCreatesOneTriggerAndPendingRunPerDueInstant(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())
	const path = "/api/v1/crons/acme%2Fwidgets%2Fnightly"

	var launched crons.RunEnvelope
	f.call(http.MethodPost, path+"/run", f.writer, nil, http.StatusOK, &launched)
	if launched.RunID == "" {
		t.Fatal("run now named no run")
	}

	ctx := context.Background()
	trigger, err := f.store.GetTrigger(ctx, launched.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerSource != "schedule" {
		t.Errorf("trigger source = %q, want schedule", trigger.TriggerSource)
	}
	if trigger.RepoURL != cronTestRepoURL || trigger.GitSHA != cronTestSHA || trigger.GitBranch != "main" {
		t.Errorf("trigger git = %+v, want the pushed source", trigger)
	}
	if trigger.Args["depth"] != "deep" {
		t.Errorf("trigger args = %v, want the schedule's", trigger.Args)
	}
	scheduleID := launched.Schedule.ID
	if trigger.TriggerEnv[crons.ScheduleEnvKey] != scheduleID {
		t.Errorf("trigger env = %v, want the schedule id under %s", trigger.TriggerEnv, crons.ScheduleEnvKey)
	}
	if trigger.IdempotencyKey == "" {
		t.Error("a scheduled launch carries no idempotency key")
	}
	run, err := f.store.GetRun(ctx, launched.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "pending" {
		t.Errorf("run status = %q, want pending", run.Status)
	}
}

func TestControllerCrons_ASecondLaunchOfOneDueInstantReachesTheFirstRun(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	ctx := context.Background()
	svc := &crons.Service{Store: f.store}
	sched, err := svc.Resolve(ctx, "acme/widgets/nightly")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	due := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	key := controller.CronIdempotencyKey(sched.ID, due)
	if key != sched.ID+"@2026-03-01T03:00:00Z" {
		t.Fatalf("idempotency key = %q", key)
	}
	first := "run_first"
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: first, Pipeline: sched.Pipeline, CreatedAt: time.Now(), IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	found, err := f.store.FindTriggerByIdempotencyKey(ctx, sched.Pipeline, key)
	if err != nil {
		t.Fatalf("FindTriggerByIdempotencyKey: %v", err)
	}
	if found.ID != first {
		t.Errorf("resolved run = %s, want %s", found.ID, first)
	}
}

func TestControllerCrons_DisarmAndDeleteRepo(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	var env crons.ScheduleEnvelope
	f.call(http.MethodPost, "/api/v1/crons/acme%2Fwidgets%2Fsweep%2Fquick/disarm", f.writer, nil,
		http.StatusOK, &env)
	if env.Schedule.Name != "acme/widgets/sweep/quick" {
		t.Errorf("disarm returned %+v", env.Schedule)
	}

	var removed map[string]any
	f.call(http.MethodDelete, "/api/v1/crons/repos?repo_url="+cronTestRepoURL, f.writer, nil,
		http.StatusOK, &removed)
	if removed["removed"] != float64(1) {
		t.Errorf("removed = %v, want the one remaining row", removed["removed"])
	}
	var overview crons.OverviewView
	f.call(http.MethodGet, "/api/v1/crons", f.reader, nil, http.StatusOK, &overview)
	if len(overview.Schedules) != 0 {
		t.Errorf("overview still holds %d schedules", len(overview.Schedules))
	}
	f.call(http.MethodDelete, "/api/v1/crons/repos?repo_url=", f.writer, nil, http.StatusBadRequest, nil)
}

func TestControllerCrons_ScopesAreEnforced(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	writes := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPut, "/api/v1/crons/repos", cronPushBody()},
		{http.MethodDelete, "/api/v1/crons/repos?repo_url=" + cronTestRepoURL, nil},
		{http.MethodPost, "/api/v1/crons/acme%2Fwidgets%2Fnightly/pause", nil},
		{http.MethodPost, "/api/v1/crons/acme%2Fwidgets%2Fnightly/resume", nil},
		{http.MethodPost, "/api/v1/crons/acme%2Fwidgets%2Fnightly/run", nil},
		{http.MethodPost, "/api/v1/crons/acme%2Fwidgets%2Fnightly/disarm", nil},
		{http.MethodPut, "/api/v1/crons/acme%2Fwidgets%2Fnightly/override", map[string]any{"cron": "0 5 * * *"}},
		{http.MethodDelete, "/api/v1/crons/acme%2Fwidgets%2Fnightly/override", nil},
	}
	for _, w := range writes {
		if got := f.status(w.method, w.path, f.reader, w.body); got != http.StatusForbidden {
			t.Errorf("%s %s with runs.read = %d, want 403", w.method, w.path, got)
		}
	}
	for _, path := range []string{"/api/v1/crons", "/api/v1/crons/acme%2Fwidgets%2Fnightly"} {
		if got := f.status(http.MethodGet, path, f.none, nil); got != http.StatusForbidden {
			t.Errorf("GET %s without runs.read = %d, want 403", path, got)
		}
	}
}

func TestControllerCrons_PushRefusesABranchGitWouldNotAccept(t *testing.T) {
	f := newCronsFixture(t)
	for _, branch := range []string{
		"has a space", "feature/../main", "-leading-dash", "trailing/", "tip.lock",
		"ref@{0}", "double//slash", "star*",
	} {
		body := cronPushBody()
		body["branch"] = branch
		if got := f.status(http.MethodPut, "/api/v1/crons/repos", f.writer, body); got != http.StatusBadRequest {
			t.Errorf("branch %q: status = %d, want 400", branch, got)
		}
	}
	for _, branch := range []string{"", "main", "feature/crons-v2", "release-1.2"} {
		body := cronPushBody()
		body["branch"] = branch
		if got := f.status(http.MethodPut, "/api/v1/crons/repos", f.writer, body); got != http.StatusOK {
			t.Errorf("branch %q: status = %d, want 200", branch, got)
		}
	}
}

func TestControllerCrons_HealthIsUnhealthyBeforeTheFirstTick(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	var overview crons.OverviewView
	f.call(http.MethodGet, "/api/v1/crons", f.reader, nil, http.StatusOK, &overview)
	if !overview.Health.TickStale {
		t.Error("a controller that has never ticked reports a fresh tick")
	}
	if crons.HealthFromView(overview.Health).Healthy() {
		t.Error("a controller with armed schedules and no tick reports healthy")
	}
}

func TestControllerCrons_OverrideRoundTripsAnEmptyArgumentSet(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	var envelope crons.ScheduleEnvelope
	f.call(http.MethodPut, "/api/v1/crons/acme%2Fwidgets%2Fnightly/override", f.writer,
		map[string]any{"args": map[string]string{}}, http.StatusOK, &envelope)
	if len(envelope.Schedule.Effective.Args) != 0 {
		t.Errorf("effective args = %v, want none", envelope.Schedule.Effective.Args)
	}
	fields := envelope.Schedule.Override.Fields
	if len(fields) != 1 || fields[0] != "args" {
		t.Fatalf("override fields = %v, want [args]", fields)
	}
	row := crons.RowFromView(envelope.Schedule)
	if row.Override == nil || row.Override.Args == nil || len(row.Override.Args) != 0 {
		t.Errorf("rebuilt override args = %#v, want a set but empty map", row.Override)
	}
	if len(row.Effective.Args) != 0 {
		t.Errorf("rebuilt effective args = %v, want none", row.Effective.Args)
	}
}

func TestControllerCrons_OverrideValuesSurviveTheWire(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	var envelope crons.ScheduleEnvelope
	f.call(http.MethodPut, "/api/v1/crons/acme%2Fwidgets%2Fnightly/override", f.writer,
		map[string]any{"cron": "*/15 * * * *", "tz": "America/Denver", "overlap": "queue", "catch_up": "6h"},
		http.StatusOK, &envelope)

	row := crons.RowFromView(envelope.Schedule)
	if row.Override == nil {
		t.Fatal("the wire carried no override")
	}
	if row.Override.Cron != "*/15 * * * *" || row.Override.TZ != "America/Denver" ||
		row.Override.Overlap != "queue" {
		t.Errorf("rebuilt override = %+v, want the values that were set", row.Override)
	}
	if row.Override.CatchUp == nil || *row.Override.CatchUp != 6*time.Hour {
		t.Errorf("rebuilt catch-up = %v, want 6h", row.Override.CatchUp)
	}
	if row.Override.Args != nil {
		t.Errorf("rebuilt args = %v, want nil for an override that names none", row.Override.Args)
	}
}

func TestControllerCrons_DetailServesTheWholeRetainedHistory(t *testing.T) {
	f := newCronsFixture(t)
	pushed := f.push(cronPushBody())
	schedules, _ := pushed["schedules"].([]any)
	first, _ := schedules[0].(map[string]any)
	id, _ := first["id"].(string)
	if id == "" {
		t.Fatalf("push returned no schedule id: %v", pushed)
	}

	ctx := context.Background()
	base := time.Now().UTC().Add(-100 * time.Hour)
	const seeded = 60
	for i := 0; i < seeded; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		fire := &store.CronFire{
			DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeMissed, Detail: "seeded",
		}
		if err := f.store.ResolveCronDue(ctx, id, at, nil, fire, at); err != nil {
			t.Fatalf("seed fire %d: %v", i, err)
		}
	}

	var detail crons.DetailView
	f.call(http.MethodGet, "/api/v1/crons/"+id, f.reader, nil, http.StatusOK, &detail)
	if len(detail.Fires) != seeded {
		t.Errorf("detail carried %d fires, want the %d the store retains so `--fires N` is not capped below N",
			len(detail.Fires), seeded)
	}
}

func TestControllerCrons_APushWithNoSchedulesKeyWithdrawsEverything(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	// safety: `schedules` is optional in the schema for exactly this -- the body
	// is the whole set, so one carrying none withdraws them all.
	out := f.push(map[string]any{"repo_url": cronTestRepoURL, "branch": "main", "sha": cronTestSHA})
	withdrawn, _ := out["withdrawn"].([]any)
	if len(withdrawn) != 2 {
		t.Fatalf("withdrawn = %v, want both schedules", out["withdrawn"])
	}

	var overview crons.OverviewView
	f.call(http.MethodGet, "/api/v1/crons", f.reader, nil, http.StatusOK, &overview)
	for _, view := range overview.Schedules {
		if view.Declared {
			t.Errorf("%s is still declared after a push that carried nothing", view.Name)
		}
	}
}
