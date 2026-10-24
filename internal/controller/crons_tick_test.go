package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func newStoreForTick(t *testing.T) *store.Store {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	cfg := githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/widgets/installation":
			body = `{"id":7,"app_id":1}`
		case "POST /app/installations/7/access_tokens":
			body = `{"token":"token","expires_at":"2100-01-01T00:00:00Z"}`
		case "GET /installation/repositories":
			body = `{"total_count":1,"repositories":[{"id":701,"full_name":"acme/widgets","owner":{"id":1,"login":"acme","type":"Organization"}}]}`
		case "GET /repos/acme/widgets/contents/.sparkwing/sparkwing.yaml":
			if r.URL.Query().Get("ref") != "0123456789abcdef0123456789abcdef01234567" {
				return nil, fmt.Errorf("configuration read at unexpected ref: %s", r.URL)
			}
			status = http.StatusNotFound
		default:
			return nil, fmt.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	fixture := &serverFixture{}
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), fixture.option(deployment.Config{}, func(s *Server) { s.WithGitHubApp(cfg) }))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func armPushedForTick(t *testing.T, st *store.Store) store.CronSchedule {
	t.Helper()
	seedTickAppConsent(t, st, store.DefaultTeam)
	return armAppRowForTick(t, teststore.ForTeam(t, st, store.DefaultTeam), "schedule-owner")
}

func armAppRowForTick(t *testing.T, tenant *store.Tenant, armedBy string) store.CronSchedule {
	t.Helper()
	next := time.Now().Truncate(time.Minute).Add(time.Minute)
	row, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID:       crons.AppScheduleID(tenant.Team(), 7, 701, "nightly", store.CronScheduleDefaultName),
		RepoPath: "https://github.com/acme/widgets.git", Pipeline: "nightly", Name: store.CronScheduleDefaultName,
		Cron: "* * * * *", TZ: "UTC", Overlap: store.CronOverlapSkip, CatchUp: time.Hour,
		Where: store.CronWhereController, GitBranch: "main", LockedRef: "0123456789abcdef0123456789abcdef01234567",
		GitHubInstallationID: 7, GitHubRepositoryID: 701, ArmedBy: armedBy, NextDueAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestCronTick_TwoControllersSharingAStoreFireOneTrigger(t *testing.T) {
	st := newStoreForTick(t)
	sched := armPushedForTick(t, st)

	// safety: both evaluators read the same instant two minutes on, so one due
	// instant exists without the test waiting a minute for the clock.
	ahead := func() time.Time { return time.Now().Add(2 * time.Minute) }
	alpha := New(st, nil)
	alpha.cronHolder, alpha.cronNow = "alpha", ahead
	beta := New(st, nil)
	beta.cronHolder, beta.cronNow = "beta", ahead

	ctx := context.Background()
	var mu sync.Mutex
	holding := map[string]bool{}
	peak := 0
	// safety: cronNow is only read from inside the leased tick, so marking the
	// holder there measures the window the lease is supposed to keep to one.
	watch := func(holder string) func() time.Time {
		return func() time.Time {
			mu.Lock()
			holding[holder] = true
			if len(holding) > peak {
				peak = len(holding)
			}
			mu.Unlock()
			return time.Now().Add(2 * time.Minute)
		}
	}
	alpha.cronNow, beta.cronNow = watch("alpha"), watch("beta")

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, c := range []*Server{alpha, beta} {
		wg.Add(1)
		go func(srv *Server) {
			defer wg.Done()
			<-start
			srv.cronTickOnce(ctx)
			mu.Lock()
			delete(holding, srv.cronHolder)
			mu.Unlock()
		}(c)
	}
	close(start)
	wg.Wait()

	if peak > 1 {
		t.Errorf("%d controllers held the tick at once, want at most 1", peak)
	}

	triggers, err := teststore.ForTeam(t, st, store.DefaultTeam).ListTriggers(ctx, store.TriggerFilter{Pipelines: []string{"nightly"}})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want one", len(triggers))
	}
	trigger := triggers[0]
	if trigger.TriggerSource != "schedule" {
		t.Errorf("trigger source = %q, want %q", trigger.TriggerSource, "schedule")
	}
	sealed, err := teststore.ForTeam(t, st, store.DefaultTeam).ReadRun(ctx, trigger.ID)
	if err != nil || sealed.Request.AuthorityRef.Kind != "schedule" || sealed.Request.AuthorityRef.ID != sched.ID {
		t.Fatalf("sealed schedule origin = %+v, err=%v", sealed.Request.AuthorityRef, err)
	}
	if trigger.IdempotencyKey == "" {
		t.Error("the launch carried no idempotency key")
	}
	run, err := teststore.ForTeam(t, st, store.DefaultTeam).ReadRun(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != runstate.RunPending || run.Request.Hash == "" || run.Request.Request.Origin != runspec.OriginSchedule || run.Request.Request.Actor.ID != sched.ArmedBy {
		t.Errorf("scheduled run = %+v", run)
	}
	stored, err := teststore.ForTeam(t, st, store.DefaultTeam).GetCronSchedule(ctx, sched.ID)
	if err != nil || stored.LastRunID != trigger.ID || stored.LastOutcome != store.CronOutcomeFired {
		t.Fatalf("stored schedule = %+v: %v", stored, err)
	}

	tick, err := st.AsOperator().GetCronTick(ctx)
	if err != nil {
		t.Fatalf("GetCronTick: %v", err)
	}
	if tick.At.IsZero() {
		t.Error("the tick recorded nothing")
	}
	if tick.Host != "alpha" && tick.Host != "beta" {
		t.Errorf("tick host = %q, want one of the two holders", tick.Host)
	}
}

func TestCronTick_ARepeatedInstantReachesTheFirstRun(t *testing.T) {
	st := newStoreForTick(t)
	sched := armPushedForTick(t, st)

	srv := New(st, nil)
	launcher := cronLauncher{server: srv}
	due := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	first, err := launcher.Launch(context.Background(), sched, due)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	second, err := launcher.Launch(context.Background(), sched, due)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if second != first {
		t.Errorf("the repeated instant started run %s, want the first run %s", second, first)
	}
	if triggers, err := teststore.ForTeam(t, st, store.DefaultTeam).ListTriggers(context.Background(), store.TriggerFilter{Pipelines: []string{"nightly"}}); err != nil || len(triggers) != 1 {
		t.Errorf("recorded %d runs (%v), want 1", len(triggers), err)
	}
}

func TestCronLauncherActive_TreatsAnUnclaimedRunAsStale(t *testing.T) {
	st := newStoreForTick(t)

	ctx := context.Background()
	now := time.Now()

	if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run_old", Pipeline: "nightly", Status: "pending", CreatedAt: now.Add(-2 * time.Hour), StartedAt: now.Add(-2 * time.Hour)}}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	launcher := cronLauncher{server: New(st, nil)}
	sched := store.CronSchedule{Team: store.DefaultTeam}

	fresh, err := launcher.Active(ctx, sched, "run_old", 4*time.Hour)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !fresh {
		t.Error("a run queued inside the window is not active")
	}
	stale, err := launcher.Active(ctx, sched, "run_old", time.Minute)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if stale {
		t.Error("a run nothing claimed for two hours still reads as active")
	}
	missing, err := launcher.Active(ctx, sched, "run_gone", time.Minute)
	if err != nil {
		t.Fatalf("Active on a missing run: %v", err)
	}
	if missing {
		t.Error("a run the store does not hold reads as active")
	}
}

// The tick evaluates every team's schedules, and a schedule team B armed
// launches its run, its trigger and its history in team B.
func TestCronTick_AScheduleFiresInTheTeamThatArmedIt(t *testing.T) {
	st := newStoreForTick(t)
	ctx := context.Background()
	teststore.SeedTeam(t, st, "team-b")
	tenantB, err := st.ForTeam(ctx, "team-b")
	if err != nil {
		t.Fatalf("ForTeam team-b: %v", err)
	}
	tenantDefault, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatalf("ForTeam default: %v", err)
	}
	seedTickAppConsent(t, st, "team-b")
	id := armAppRowForTick(t, tenantB, "team-b-owner").ID

	srv := New(st, nil)
	srv.cronNow = func() time.Time { return time.Now().Add(2 * time.Minute) }
	srv.cronTickOnce(ctx)

	sched, err := tenantB.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("team B's schedule: %v", err)
	}
	if sched.LastRunID == "" || sched.LastOutcome != store.CronOutcomeFired {
		t.Fatalf("the tick did not fire team B's schedule: outcome %q run %q", sched.LastOutcome, sched.LastRunID)
	}
	if _, err := tenantB.GetRun(ctx, sched.LastRunID); err != nil {
		t.Errorf("team B cannot read the run its schedule fired: %v", err)
	}
	if _, err := tenantDefault.GetRun(ctx, sched.LastRunID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default team GetRun of team B's fired run = %v, want ErrNotFound", err)
	}
	fires, err := tenantB.ListCronFires(ctx, id, 0)
	if err != nil || !slices.ContainsFunc(fires, func(f store.CronFire) bool { return f.RunID == sched.LastRunID }) {
		t.Errorf("team B's fire history = %+v (err %v), want the fired run in it", fires, err)
	}
	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetCronSchedule(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the default team reads team B's schedule: %v", err)
	}
}

func TestCronLauncherRejectsChangedScheduleSnapshotsProperty(t *testing.T) {
	st := newStoreForTick(t)
	sched := armPushedForTick(t, st)
	teststore.SeedTeam(t, st, "other")
	srv := New(st, nil)
	launcher := srv.ScheduleLauncher()
	due := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	first, err := launcher.Launch(t.Context(), sched, due)
	if err != nil {
		t.Fatal(err)
	}
	property := func(forged string) bool {
		input := sched
		input.Pipeline, input.RepoPath, input.GitBranch, input.LockedRef, input.ArmedBy = forged, forged, forged, forged, forged
		input.Args = map[string]string{"forged": forged}
		runID, err := launcher.Launch(t.Context(), input, due.In(time.FixedZone("offset", 3600)))
		if err == nil || runID != "" {
			t.Errorf("changed schedule snapshot replayed: %s %v", runID, err)
			return false
		}
		runID, err = launcher.Launch(t.Context(), sched, due.In(time.FixedZone("offset", 3600)))
		if err != nil || runID != first {
			t.Errorf("unchanged snapshot did not replay: %s %v", runID, err)
			return false
		}
		row, err := teststore.ForTeam(t, st, sched.Team).ReadRun(t.Context(), runID)
		request := row.Request.Request
		if err != nil || request.Pipeline != sched.Pipeline || request.Actor.ID != sched.ArmedBy || request.Git.Ref != "refs/heads/main" || request.Git.SHA != sched.LockedRef || request.Source.Location.RemoteURL != sched.RepoPath || request.RequestedArgs["forged"] != "" {
			t.Errorf("forged launch changed the stored request: %+v: %v", request, err)
			return false
		}
		input.Team = "other"
		if id, err := launcher.Launch(t.Context(), input, due); id != "" || !errors.Is(err, store.ErrNotFound) {
			t.Errorf("foreign schedule admission = %s: %v", id, err)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
	for _, team := range []store.Team{sched.Team, "other"} {
		rows, err := teststore.ForTeam(t, st, team).ListRuns(t.Context(), store.RunFilter{})
		want := 0
		if team == sched.Team {
			want = 1
		}
		if err != nil || len(rows) != want {
			t.Fatalf("team %s runs = %d, want %d: %v", team, len(rows), want, err)
		}
	}
}

func TestCronLauncherRequiresTheTenantTransactionBackend(t *testing.T) {
	st := newStoreForTick(t)
	sched := armPushedForTick(t, st)
	srv := New(st, nil)
	testOnlyTenantPort[ScheduledRunWriter](srv, func(context.Context, store.Team) (ScheduledRunWriter, error) { return nil, nil })
	due := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	_, err := srv.ScheduleLauncher().Launch(t.Context(), sched, due)
	var missing *apierr.Error
	if !errors.As(err, &missing) || missing.Code != apierr.PolicyMissing {
		t.Fatalf("missing backend: %v", err)
	}
	other := teststore.ForTeam(t, st, store.DefaultTeam)
	testOnlyTenantPort[ScheduledRunWriter](srv, func(context.Context, store.Team) (ScheduledRunWriter, error) { return other, nil })
	sched.Team = "other"
	if _, err := srv.ScheduleLauncher().Launch(t.Context(), sched, due); !errors.As(err, &missing) || missing.Code != apierr.PolicyMissing {
		t.Fatalf("wrong-team backend = %v", err)
	}
	if runs, err := other.ListRuns(t.Context(), store.RunFilter{}); err != nil || len(runs) != 0 {
		t.Fatalf("wrong-team backend committed runs=%v: %v", runs, err)
	}
}

func seedTickAppConsent(t *testing.T, st *store.Store, team store.Team) {
	t.Helper()
	engineSQL(t, st, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,?,1,'acme','Organization',1,1)`, team)
	engineSQL(t, st, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES (?,'gh:701',701,'acme/widgets',7,'schedule-owner',1)`, team)
}
