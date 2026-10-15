package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type scheduleWriterFake struct {
	*store.Tenant
	s         *Server
	runs      map[string]string
	seen      capability.Authorized
	failure   error
	lastError error
}

func (f *scheduleWriterFake) service(a capability.Authorized) *crons.Service {
	f.seen = a
	return f.s.cronServiceFor(f.Tenant, a.Principal.Capability.Subject.ID)
}

func (f *scheduleWriterFake) ReplaceAuthorizedSchedules(ctx context.Context, a capability.Authorized, body controllerwire.ScheduleRepoRequest) ([]string, []string, error) {
	if f.failure != nil {
		return nil, nil, f.failure
	}
	entries := make([]cronRepoSchedule, 0, len(body.Schedules))
	for _, entry := range body.Schedules {
		entries = append(entries, cronRepoSchedule(entry))
	}
	declared, err := declaredFromRequest(entries)
	if err != nil {
		return nil, nil, err
	}
	svc := f.service(a)
	report, err := svc.ArmPushed(ctx, crons.ArmPush{RepoURL: body.RepoURL, Branch: body.Branch, SHA: body.SHA, Entries: declared, GitHubInstallationID: 17, GitHubRepositoryID: 23})
	f.lastError = err
	ids := make([]string, 0, len(report.Schedules))
	for _, row := range report.Schedules {
		ids = append(ids, row.ID)
	}
	return ids, report.Withdrawals, err
}

func (f *scheduleWriterFake) DeleteAuthorizedSchedules(ctx context.Context, a capability.Authorized, repo string) (int, error) {
	return f.service(a).DisarmRepoURL(ctx, repo)
}

func (f *scheduleWriterFake) PauseAuthorizedSchedule(ctx context.Context, a capability.Authorized, name string, pause bool) (string, error) {
	svc := f.service(a)
	row, err := svc.Resolve(ctx, name)
	if err != nil {
		return "", err
	}
	if pause {
		err = svc.Pause(ctx, row.ID)
	} else {
		err = svc.Resume(ctx, row.ID)
	}
	return row.ID, err
}

func (f *scheduleWriterFake) OverrideAuthorizedSchedule(ctx context.Context, a capability.Authorized, name string, body *controllerwire.ScheduleOverride) (string, error) {
	svc := f.service(a)
	sched, err := svc.Resolve(ctx, name)
	if err != nil {
		return "", err
	}
	if body == nil {
		_, err = svc.ClearOverride(ctx, sched.ID)
	} else {
		fields := crons.Override{Cron: body.Cron, TZ: body.TZ, Overlap: body.Overlap}
		if body.CatchUp != nil {
			d, parseErr := time.ParseDuration(*body.CatchUp)
			if parseErr != nil {
				return "", parseErr
			}
			fields.CatchUp = &d
		}
		if body.Args != nil {
			fields.Args = *body.Args
		}
		_, err = svc.SetOverride(ctx, sched.ID, fields)
	}
	return sched.ID, err
}

func (f *scheduleWriterFake) RunAuthorizedSchedule(ctx context.Context, a capability.Authorized, name, key string) (string, string, bool, error) {
	sched, err := f.service(a).Resolve(ctx, name)
	if err != nil {
		return "", "", false, err
	}
	replayKey := string(f.Team()) + "/" + sched.ID + "/" + key
	if run, ok := f.runs[replayKey]; ok {
		return sched.ID, run, true, nil
	}
	run := "run-" + key
	f.runs[replayKey] = run
	return sched.ID, run, false, nil
}

func (f *scheduleWriterFake) DeleteAuthorizedSchedule(ctx context.Context, a capability.Authorized, name string) (string, bool, error) {
	svc := f.service(a)
	sched, err := svc.Resolve(ctx, name)
	if err != nil {
		return "", false, err
	}
	err = svc.Disarm(ctx, sched.ID)
	return sched.ID, err == nil, err
}

type scheduleHTTPFixture struct {
	s      *Server
	writer *scheduleWriterFake
	raw    string
	facts  *reportingFacts
}

func newScheduleHTTPFixture(t *testing.T) scheduleHTTPFixture {
	t.Helper()
	s := New(newStoreForAuth(t), nil)
	tenant, err := s.store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	writer := &scheduleWriterFake{Tenant: tenant, s: s, runs: map[string]string{}}
	s.WithScheduleWriter(func(context.Context, store.Team) (ScheduleWriter, error) { return writer, nil })
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	now := time.Now().UTC()
	p := capability.Principal{
		Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"},
		Capability: capability.Capability{
			Prefix: "root", Digest: sha256.Sum256([]byte(raw)), Team: string(tenant.Team()),
			Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "pusher"},
			Issuance: capability.Delegate, Binding: capability.Binding{Kind: capability.BindingNone},
			Grants: []capability.Grant{capability.RunsRead, capability.RunsControl},
		},
	}
	facts := &reportingFacts{digest: p.Capability.Digest, snapshot: capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: p, MembershipValid: true}}}}
	s.WithAuthorization(RouteAuthorization{Authenticator: capability.NewAuthenticator(facts), Bindings: facts})
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
		t.Fatalf("%s %s: HTTP %d %s, want %d; fake error=%v", method, path, w.Code, w.Body, want, f.writer.lastError)
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
	if len(pushed.Schedules) != 2 || f.writer.seen.Target.Team != string(store.DefaultTeam) || f.writer.seen.Principal.Capability.Subject.ID != "pusher" {
		t.Fatalf("push=%+v authority=%+v", pushed, f.writer.seen)
	}
	var list controllerwire.ScheduleOverview
	f.call(t, "GET", "/api/v1/schedules?next=2&team=foreign", "", "", 200, &list)
	if len(list.Schedules) != 2 || len(list.Upcoming) != 4 || !list.Health.TickStale || list.Health.Armed != 2 {
		t.Fatalf("list=%+v", list)
	}
	var detail controllerwire.DetailView
	f.call(t, "GET", scheduleHTTPPath+"?fires=0&next=2", "", "", 200, &detail)
	if len(detail.Upcoming) != 2 || detail.Fires == nil || len(detail.Fires) != 0 || detail.Schedule.Effective.Args["depth"] != "deep" {
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
	if first.Replayed || !replay.Replayed || first.RunID != replay.RunID || first.Schedule.ID != replay.Schedule.ID || len(f.writer.runs) != 1 {
		t.Fatalf("first=%+v replay=%+v", first, replay)
	}
	f.call(t, "POST", scheduleHTTPPath+"/run", "", "", 400, nil)
	f.call(t, "GET", "/api/v1/schedules/crn_missing", "", "", 404, nil)
	f.call(t, "GET", "/api/v1/schedules/missing", "", "", 404, nil)
}

func TestScheduleHTTPOverrideAndWithdrawal(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 200, nil)
	var out controllerwire.ScheduleEnvelope
	f.call(t, "PUT", scheduleHTTPPath+"/override", `{"cron":"*/15 * * * *","tz":"America/Denver","overlap":"queue","catch_up":"0s","args":{}}`, "", 200, &out)
	if out.Schedule.Effective.Cron != "*/15 * * * *" || out.Schedule.Effective.TZ != "America/Denver" || out.Schedule.Effective.Overlap != "queue" || out.Schedule.Effective.CatchUpNS != 0 || len(out.Schedule.Effective.Args) != 0 || len(out.Schedule.Override.Fields) != 5 {
		t.Fatalf("override=%+v", out)
	}
	f.call(t, "DELETE", scheduleHTTPPath+"/override", "", "", 200, &out)
	if out.Schedule.Effective.Cron != "0 3 * * *" || out.Schedule.Effective.Args["depth"] != "deep" || len(out.Schedule.Override.Fields) != 0 {
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
	f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 403, nil)
	rows, err := f.writer.ListCronSchedules(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed transaction leaked schedules: %+v %v", rows, err)
	}
	f.s.WithScheduleWriter(nil)
	w := f.call(t, "PUT", "/api/v1/schedules/repos", schedulePushJSON, "", 500, nil)
	if !strings.Contains(w.Body.String(), "policy_missing") {
		t.Fatal(w.Body.String())
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
		if err := f.writer.ResolveCronDue(t.Context(), id, at, nil, fire, at); err != nil {
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
