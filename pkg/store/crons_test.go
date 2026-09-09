package store_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var cronBase = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func armCron(t *testing.T, st *store.Store, id, repo, pipeline string, at time.Time) store.CronSchedule {
	t.Helper()
	next := at.Add(time.Hour)
	sched, created, err := st.ArmCronSchedule(context.Background(), store.CronSchedule{
		ID: id, RepoPath: repo, Pipeline: pipeline,
		Cron: "0 * * * *", TZ: "UTC", Overlap: store.CronOverlapSkip,
		CatchUp: time.Hour, ArmedBy: "korey", NextDueAt: &next,
	}, at)
	if err != nil {
		t.Fatalf("ArmCronSchedule(%s): %v", id, err)
	}
	if !created {
		t.Fatalf("ArmCronSchedule(%s) reported an existing row on a fresh store", id)
	}
	return sched
}

func TestArmCronScheduleInsertsThenRefreshesWithoutClobberingState(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	if !sched.ArmedAt.Equal(cronBase) || !sched.CursorAt.Equal(cronBase) || sched.ArmedBy != "korey" {
		t.Fatalf("armed row = %+v, want armed_at/cursor_at %v", sched, cronBase)
	}
	if !sched.Declared || sched.Paused {
		t.Fatalf("armed row declared=%v paused=%v, want declared and unpaused", sched.Declared, sched.Paused)
	}
	if sched.LastFiredAt != nil || sched.LastOutcome != "" {
		t.Fatalf("armed row carries fire history %+v", sched)
	}

	if err := st.SetCronSchedulePaused(ctx, sched.ID, true, cronBase.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	fired := cronBase.Add(2 * time.Minute)
	if err := st.ResolveCronDue(ctx, sched.ID, fired, nil, &store.CronFire{
		DueAt: fired, DecidedAt: fired, Outcome: store.CronOutcomeFired, RunID: "run-1",
	}, fired); err != nil {
		t.Fatal(err)
	}

	rearmedAt := cronBase.Add(time.Hour)
	next := rearmedAt.Add(6 * time.Hour)
	again, created, err := st.ArmCronSchedule(ctx, store.CronSchedule{
		ID: "crn_ignored", RepoPath: "/repo/one", Pipeline: "nightly",
		Cron: "0 3 * * *", TZ: "America/Boise", Overlap: store.CronOverlapQueue,
		CatchUp: 2 * time.Hour, ArmedBy: "someone-else", NextDueAt: &next,
	}, rearmedAt)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("re-arm reported a create")
	}
	if again.ID != sched.ID || !again.ArmedAt.Equal(cronBase) || again.ArmedBy != "korey" {
		t.Errorf("re-arm rewrote identity or arming: %+v", again)
	}
	if again.Cron != "0 3 * * *" || again.TZ != "America/Boise" ||
		again.Overlap != store.CronOverlapQueue || again.CatchUp != 2*time.Hour {
		t.Errorf("re-arm did not republish the declaration: %+v", again)
	}
	if !again.Paused || !again.CursorAt.Equal(fired) {
		t.Errorf("re-arm clobbered pause or cursor: paused=%v cursor=%v", again.Paused, again.CursorAt)
	}
	if again.LastRunID != "run-1" || again.LastOutcome != store.CronOutcomeFired || again.LastFiredAt == nil {
		t.Errorf("re-arm clobbered the last fire: %+v", again)
	}
	if again.NextDueAt == nil || !again.NextDueAt.Equal(next) {
		t.Errorf("re-arm next due = %v, want %v", again.NextDueAt, next)
	}
	if !again.UpdatedAt.Equal(rearmedAt) {
		t.Errorf("re-arm updated_at = %v, want %v", again.UpdatedAt, rearmedAt)
	}
}

func TestArmCronScheduleRedeclaresAWithdrawnSchedule(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	if err := st.SetCronScheduleDeclared(ctx, sched.ID, false, cronBase.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Declared {
		t.Fatal("SetCronScheduleDeclared(false) left the schedule declared")
	}
	if _, _, err := st.ArmCronSchedule(ctx, store.CronSchedule{
		ID: sched.ID, RepoPath: "/repo/one", Pipeline: "nightly",
		Cron: "0 * * * *", TZ: "UTC", Overlap: store.CronOverlapSkip, CatchUp: time.Hour,
	}, cronBase.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronSchedule(ctx, sched.ID); err != nil || !got.Declared {
		t.Fatalf("re-arm left declared=%v (err %v)", got.Declared, err)
	}
}

func TestListCronSchedulesOrdersByRepoThenPipeline(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	armCron(t, st, "crn_b2", "/repo/two", "build", cronBase)
	armCron(t, st, "crn_a2", "/repo/one", "release", cronBase)
	armCron(t, st, "crn_a1", "/repo/one", "nightly", cronBase)

	got, err := st.ListCronSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sched := range got {
		ids = append(ids, sched.ID)
	}
	want := []string{"crn_a1", "crn_a2", "crn_b2"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

func TestGetCronScheduleMissingIsNotFound(t *testing.T) {
	st := storetest.Open(t)
	if _, err := st.GetCronSchedule(context.Background(), "crn_absent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCronSchedule = %v, want ErrNotFound", err)
	}
}

func TestCronScheduleMutatorsReportAnUnknownID(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	next := cronBase.Add(time.Hour)
	for name, err := range map[string]error{
		"pause":    st.SetCronSchedulePaused(ctx, "crn_absent", true, cronBase),
		"declared": st.SetCronScheduleDeclared(ctx, "crn_absent", false, cronBase),
		"next due": st.SetCronScheduleNextDue(ctx, "crn_absent", &next, cronBase),
		"resolve":  st.ResolveCronDue(ctx, "crn_absent", cronBase, nil, nil, cronBase),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s = %v, want ErrNotFound", name, err)
		}
	}
}

func TestSetCronSchedulePausedRoundTrips(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	paused := cronBase.Add(time.Minute)
	if err := st.SetCronSchedulePaused(ctx, sched.ID, true, paused); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Paused || !got.UpdatedAt.Equal(paused) {
		t.Fatalf("paused = %v, updated = %v, want true at %v", got.Paused, got.UpdatedAt, paused)
	}
	resumed := paused.Add(time.Minute)
	if err := st.SetCronSchedulePaused(ctx, sched.ID, false, resumed); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronSchedule(ctx, sched.ID); err != nil || got.Paused {
		t.Fatalf("resume left paused=%v (err %v)", got.Paused, err)
	}
}

func TestSetCronScheduleNextDueClearsAndSets(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	if sched.NextDueAt == nil {
		t.Fatal("armed schedule lost its next due instant")
	}
	if err := st.SetCronScheduleNextDue(ctx, sched.ID, nil, cronBase.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextDueAt != nil {
		t.Fatalf("next due = %v, want nil for an expression that never matches again", got.NextDueAt)
	}
	next := cronBase.Add(24 * time.Hour)
	if err := st.SetCronScheduleNextDue(ctx, sched.ID, &next, cronBase.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronSchedule(ctx, sched.ID); err != nil || got.NextDueAt == nil || !got.NextDueAt.Equal(next) {
		t.Fatalf("next due = %v (err %v), want %v", got.NextDueAt, err, next)
	}
}

func TestResolveCronDueNeverRewindsTheCursor(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	ahead := cronBase.Add(time.Hour)
	if err := st.ResolveCronDue(ctx, sched.ID, ahead, nil, nil, ahead); err != nil {
		t.Fatal(err)
	}
	behind := cronBase.Add(time.Minute)
	if err := st.ResolveCronDue(ctx, sched.ID, behind, nil, &store.CronFire{
		DueAt: behind, DecidedAt: behind, Outcome: store.CronOutcomeFired, RunID: "run-1",
	}, behind); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CursorAt.Equal(ahead) {
		t.Errorf("cursor = %v, want the newer %v", got.CursorAt, ahead)
	}
	if got.LastRunID != "run-1" {
		t.Errorf("last run = %q, want the older resolve still recorded", got.LastRunID)
	}
}

func TestDeleteCronSchedulesForRepoCountsWhatWent(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	armCron(t, st, "crn_a1", "/repo/one", "nightly", cronBase)
	armCron(t, st, "crn_a2", "/repo/one", "release", cronBase)
	kept := armCron(t, st, "crn_b1", "/repo/two", "build", cronBase)
	at := cronBase.Add(time.Minute)
	if err := st.ResolveCronDue(ctx, "crn_a1", at, nil, &store.CronFire{
		DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeMissed, Detail: "host asleep",
	}, at); err != nil {
		t.Fatal(err)
	}

	removed, err := st.DeleteCronSchedulesForRepo(ctx, "/repo/one")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	fires, err := st.ListCronFires(ctx, "crn_a1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 0 {
		t.Errorf("repo delete left %d fires behind", len(fires))
	}
	if _, err := st.GetCronSchedule(ctx, kept.ID); err != nil {
		t.Errorf("repo delete took another repository's schedule: %v", err)
	}
	if removed, err = st.DeleteCronSchedulesForRepo(ctx, "/repo/one"); err != nil || removed != 0 {
		t.Errorf("second delete = (%d, %v), want (0, nil)", removed, err)
	}
}

func TestResolveCronDueStampsEachOutcome(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)

	firedAt := cronBase.Add(time.Hour)
	next := firedAt.Add(time.Hour)
	if err := st.ResolveCronDue(ctx, sched.ID, firedAt, &next, &store.CronFire{
		DueAt: firedAt, DecidedAt: firedAt, Outcome: store.CronOutcomeFired, RunID: "run-1",
	}, firedAt); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CursorAt.Equal(firedAt) || got.LastRunID != "run-1" ||
		got.LastOutcome != store.CronOutcomeFired || got.LastFiredAt == nil || !got.LastFiredAt.Equal(firedAt) {
		t.Fatalf("after fire = %+v", got)
	}
	if got.NextDueAt == nil || !got.NextDueAt.Equal(next) {
		t.Fatalf("after fire next due = %v, want %v", got.NextDueAt, next)
	}

	for i, outcome := range []string{
		store.CronOutcomeSkippedOverlap, store.CronOutcomeMissed, store.CronOutcomeFailed,
	} {
		at := firedAt.Add(time.Duration(i+1) * time.Hour)
		if err := st.ResolveCronDue(ctx, sched.ID, at, nil, &store.CronFire{
			DueAt: at, DecidedAt: at, Outcome: outcome, Detail: "detail " + outcome,
		}, at); err != nil {
			t.Fatalf("%s: %v", outcome, err)
		}
		got, err = st.GetCronSchedule(ctx, sched.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastOutcome != outcome || !got.CursorAt.Equal(at) {
			t.Errorf("%s left outcome=%q cursor=%v", outcome, got.LastOutcome, got.CursorAt)
		}
		if got.LastRunID != "run-1" || got.LastFiredAt == nil || !got.LastFiredAt.Equal(firedAt) {
			t.Errorf("%s overwrote the last successful launch: %+v", outcome, got)
		}
	}

	fires, err := st.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 4 {
		t.Fatalf("fires = %d, want 4", len(fires))
	}
	for _, fire := range fires {
		if fire.ID == "" || fire.ScheduleID != sched.ID {
			t.Errorf("fire %+v is not keyed to the schedule with a minted id", fire)
		}
	}
}

func TestResolveCronDueWithoutAFireOnlyMovesTheCursor(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	at := cronBase.Add(time.Hour)
	if err := st.ResolveCronDue(ctx, sched.ID, at, nil, nil, at); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CursorAt.Equal(at) || got.LastOutcome != "" || got.LastFiredAt != nil {
		t.Fatalf("cursor-only resolve = %+v", got)
	}
	fires, err := st.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 0 {
		t.Fatalf("cursor-only resolve wrote %d fires", len(fires))
	}
}

func TestResolveCronDueRefusesAnUnknownOutcome(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	err := st.ResolveCronDue(ctx, sched.ID, cronBase, nil, &store.CronFire{
		DueAt: cronBase, DecidedAt: cronBase, Outcome: "exploded",
	}, cronBase)
	if err == nil {
		t.Fatal("ResolveCronDue accepted an outcome outside the enum")
	}
}

func TestResolveCronDuePrunesFireHistory(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	const written = 210
	for i := range written {
		at := cronBase.Add(time.Duration(i+1) * time.Minute)
		if err := st.ResolveCronDue(ctx, sched.ID, at, nil, &store.CronFire{
			DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeMissed,
		}, at); err != nil {
			t.Fatalf("fire %d: %v", i, err)
		}
	}
	var kept int
	if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st,
		`SELECT COUNT(*) FROM cron_fires WHERE schedule_id = ?`), sched.ID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 200 {
		t.Fatalf("retained fires = %d, want 200", kept)
	}
	newest, err := st.ListCronFires(ctx, sched.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantNewest := cronBase.Add(written * time.Minute)
	if len(newest) != 1 || !newest[0].DecidedAt.Equal(wantNewest) {
		t.Fatalf("newest retained fire = %+v, want one decided at %v", newest, wantNewest)
	}
}

func TestListCronFiresOrderAndDefaultLimit(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	const written = 60
	for i := range written {
		at := cronBase.Add(time.Duration(i+1) * time.Minute)
		if err := st.ResolveCronDue(ctx, sched.ID, at, nil, &store.CronFire{
			DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeFailed, Detail: "boom",
		}, at); err != nil {
			t.Fatal(err)
		}
	}
	fires, err := st.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 50 {
		t.Fatalf("default limit returned %d fires, want 50", len(fires))
	}
	for i := 1; i < len(fires); i++ {
		if fires[i].DecidedAt.After(fires[i-1].DecidedAt) {
			t.Fatalf("fires are not newest first at %d: %v then %v",
				i, fires[i-1].DecidedAt, fires[i].DecidedAt)
		}
	}
	if !fires[0].DecidedAt.Equal(cronBase.Add(written * time.Minute)) {
		t.Errorf("newest fire = %v, want %v", fires[0].DecidedAt, cronBase.Add(written*time.Minute))
	}
	if fires[0].Outcome != store.CronOutcomeFailed || fires[0].Detail != "boom" {
		t.Errorf("fire round-trip = %+v", fires[0])
	}
	if fires, err = st.ListCronFires(ctx, sched.ID, 3); err != nil || len(fires) != 3 {
		t.Fatalf("explicit limit returned %d fires (err %v), want 3", len(fires), err)
	}
	if fires, err = st.ListCronFires(ctx, "crn_absent", 0); err != nil || len(fires) != 0 {
		t.Fatalf("unknown schedule returned %d fires (err %v)", len(fires), err)
	}
}

func TestCronTickRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	never, err := st.GetCronTick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !never.At.IsZero() || never.Host != "" || never.Version != "" || never.Error != "" {
		t.Fatalf("never-ticked store = %+v, want zero", never)
	}

	tick := store.CronTick{At: cronBase, Host: "boxy", Version: "v0.46.0"}
	if err := st.RecordCronTick(ctx, tick); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronTick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(cronBase) || got.Host != "boxy" || got.Version != "v0.46.0" || got.Error != "" {
		t.Fatalf("tick = %+v, want %+v", got, tick)
	}

	failed := store.CronTick{At: cronBase.Add(time.Minute), Host: "boxy", Version: "v0.46.0", Error: "lock held"}
	if err := st.RecordCronTick(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronTick(ctx); err != nil {
		t.Fatal(err)
	}
	if got.Error != "lock held" || !got.At.Equal(failed.At) {
		t.Fatalf("failed tick = %+v, want %+v", got, failed)
	}

	if err := st.RecordCronTick(ctx, store.CronTick{At: failed.At.Add(time.Minute), Host: "boxy", Version: "v0.46.0"}); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronTick(ctx); err != nil {
		t.Fatal(err)
	}
	if got.Error != "" {
		t.Fatalf("a clean tick left the previous error %q", got.Error)
	}
}

func TestArmCronScheduleRoundTripsNameWhereArgsAndLock(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	args := map[string]string{"env": "prod", "shard": "2"}
	sched, created, err := st.ArmCronSchedule(ctx, store.CronSchedule{
		ID: "crn_named", RepoPath: "/repo/one", Pipeline: "nightly", Name: "morning",
		Cron: "0 6 * * *", TZ: "UTC", CatchUp: time.Hour,
		Where: store.CronWhereController, Args: args,
		LockedRef: "abc123", LockedBinary: "/home/bin/nightly", LockedDigest: "sha256:beef",
	}, cronBase)
	if err != nil || !created {
		t.Fatalf("ArmCronSchedule = (%v, %v), want a created row", created, err)
	}
	if sched.Name != "morning" || sched.Where != store.CronWhereController {
		t.Errorf("armed row = %+v, want the declared name and where", sched)
	}
	if !maps.Equal(sched.Args, args) {
		t.Errorf("args = %v, want %v", sched.Args, args)
	}
	if sched.LockedRef != "abc123" || sched.LockedBinary != "/home/bin/nightly" || sched.LockedDigest != "sha256:beef" {
		t.Errorf("lock = %+v, want the declared pin", sched.Declaration())
	}
	if sched.Override != nil {
		t.Errorf("a freshly armed schedule carries override %+v", sched.Override)
	}
}

func TestArmCronScheduleDefaultsNameWhereAndArgs(t *testing.T) {
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	if sched.Name != store.CronScheduleDefaultName || sched.Where != store.CronWhereLocal {
		t.Fatalf("defaults = (%q, %q), want (%q, %q)",
			sched.Name, sched.Where, store.CronScheduleDefaultName, store.CronWhereLocal)
	}
	if sched.Args != nil {
		t.Fatalf("args = %v, want nil for a schedule that declares none", sched.Args)
	}
}

func TestArmCronScheduleRefusesAnUnknownWhere(t *testing.T) {
	st := storetest.Open(t)
	if _, _, err := st.ArmCronSchedule(context.Background(), store.CronSchedule{
		ID: "crn_a", RepoPath: "/repo/one", Pipeline: "nightly",
		Cron: "0 * * * *", TZ: "UTC", Where: "mars",
	}, cronBase); err == nil {
		t.Fatal("ArmCronSchedule accepted a where outside the enum")
	}
}

func TestArmCronSchedulePreservesTheOverride(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	catchUp := 30 * time.Minute
	set := cronBase.Add(time.Minute)
	if err := st.SetCronOverride(ctx, sched.ID, store.CronOverride{
		Cron: "0 4 * * *", CatchUp: &catchUp, Base: sched.Declaration(), SetAt: set,
	}, set); err != nil {
		t.Fatal(err)
	}
	again, _, err := st.ArmCronSchedule(ctx, store.CronSchedule{
		ID: sched.ID, RepoPath: "/repo/one", Pipeline: "nightly",
		Cron: "0 3 * * *", TZ: "UTC", CatchUp: time.Hour,
	}, cronBase.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.Override == nil {
		t.Fatal("re-arm dropped the host override")
	}
	if again.Override.Cron != "0 4 * * *" || again.Override.CatchUp == nil || *again.Override.CatchUp != catchUp {
		t.Errorf("re-arm rewrote the override: %+v", again.Override)
	}
	if !again.Override.SetAt.Equal(set) || again.Override.Base.Cron != "0 * * * *" {
		t.Errorf("re-arm rewrote when the override was set or what against: %+v", again.Override)
	}
	if again.Effective().Cron != "0 4 * * *" || again.Declaration().Cron != "0 3 * * *" {
		t.Errorf("re-armed declaration %q, effective %q", again.Declaration().Cron, again.Effective().Cron)
	}
}

func TestCronOverrideRoundTripsAndClears(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	set := cronBase.Add(time.Minute)
	catchUp := 15 * time.Minute
	override := store.CronOverride{
		Cron:    "*/5 * * * *",
		TZ:      "America/Boise",
		Overlap: store.CronOverlapQueue,
		CatchUp: &catchUp,
		Args:    map[string]string{"env": "staging"},
		Base:    sched.Declaration(),
		SetAt:   set,
	}
	if err := st.SetCronOverride(ctx, sched.ID, override, set); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Override == nil {
		t.Fatal("SetCronOverride stored no override")
	}
	if got.Override.Cron != override.Cron || got.Override.TZ != override.TZ ||
		got.Override.Overlap != override.Overlap || !got.Override.SetAt.Equal(set) {
		t.Errorf("override = %+v, want %+v", got.Override, override)
	}
	if got.Override.CatchUp == nil || *got.Override.CatchUp != catchUp {
		t.Errorf("override catch-up = %v, want %v", got.Override.CatchUp, catchUp)
	}
	if !maps.Equal(got.Override.Args, override.Args) {
		t.Errorf("override args = %v, want %v", got.Override.Args, override.Args)
	}
	if !reflect.DeepEqual(got.Override.Base, sched.Declaration()) {
		t.Errorf("override base = %+v, want %+v", got.Override.Base, sched.Declaration())
	}
	if !got.UpdatedAt.Equal(set) {
		t.Errorf("updated_at = %v, want %v", got.UpdatedAt, set)
	}

	// safety: an override replaces the previous one whole, so a field the new
	// one leaves unset must stop applying rather than linger.
	replaced := set.Add(time.Minute)
	if err := st.SetCronOverride(ctx, sched.ID, store.CronOverride{
		TZ: "UTC", Base: sched.Declaration(), SetAt: replaced,
	}, replaced); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronSchedule(ctx, sched.ID); err != nil {
		t.Fatal(err)
	}
	if got.Override.Cron != "" || got.Override.CatchUp != nil || got.Override.Args != nil {
		t.Errorf("replacing the override kept fields from the first: %+v", got.Override)
	}

	cleared := replaced.Add(time.Minute)
	if err := st.ClearCronOverride(ctx, sched.ID, cleared); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronSchedule(ctx, sched.ID); err != nil {
		t.Fatal(err)
	}
	if got.Override != nil {
		t.Fatalf("ClearCronOverride left %+v", got.Override)
	}
	if !reflect.DeepEqual(got.Effective(), got.Declaration()) {
		t.Errorf("effective %+v, want the declaration %+v", got.Effective(), got.Declaration())
	}
}

func TestCronOverrideDistinguishesNoArgsFromNoArgOverride(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched, _, err := st.ArmCronSchedule(ctx, store.CronSchedule{
		ID: "crn_a", RepoPath: "/repo/one", Pipeline: "nightly",
		Cron: "0 * * * *", TZ: "UTC", CatchUp: time.Hour,
		Args: map[string]string{"env": "prod"},
	}, cronBase)
	if err != nil {
		t.Fatal(err)
	}
	at := cronBase.Add(time.Minute)
	if err := st.SetCronOverride(ctx, sched.ID, store.CronOverride{
		Base: sched.Declaration(), SetAt: at,
	}, at); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Override.Args != nil {
		t.Errorf("nil override args read back as %v, want nil", got.Override.Args)
	}
	if !maps.Equal(got.Effective().Args, map[string]string{"env": "prod"}) {
		t.Errorf("effective args = %v, want the declared ones", got.Effective().Args)
	}

	if err := st.SetCronOverride(ctx, sched.ID, store.CronOverride{
		Args: map[string]string{}, Base: sched.Declaration(), SetAt: at,
	}, at); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronSchedule(ctx, sched.ID); err != nil {
		t.Fatal(err)
	}
	if got.Override.Args == nil {
		t.Fatal("an override to no arguments read back as no override at all")
	}
	if len(got.Effective().Args) != 0 {
		t.Errorf("effective args = %v, want none", got.Effective().Args)
	}
}

func TestCronScheduleEffectivePrecedence(t *testing.T) {
	catchUp := 5 * time.Minute
	sched := store.CronSchedule{
		Cron: "0 * * * *", TZ: "UTC", Overlap: store.CronOverlapSkip, CatchUp: time.Hour,
		Where: store.CronWhereLocal, Args: map[string]string{"env": "prod"},
		LockedRef: "abc123",
		Override: &store.CronOverride{
			Cron: "0 4 * * *", CatchUp: &catchUp, Args: map[string]string{"env": "staging"},
		},
	}
	effective := sched.Effective()
	if effective.Cron != "0 4 * * *" || effective.CatchUp != catchUp {
		t.Errorf("overridden fields = %+v", effective)
	}
	if effective.TZ != "UTC" || effective.Overlap != store.CronOverlapSkip {
		t.Errorf("unset override fields did not fall back: %+v", effective)
	}
	if !maps.Equal(effective.Args, map[string]string{"env": "staging"}) {
		t.Errorf("effective args = %v, want the override's", effective.Args)
	}
	if effective.Where != store.CronWhereLocal || effective.LockedRef != "abc123" {
		t.Errorf("where and lock are not overridable, got %+v", effective)
	}
	if decl := sched.Declaration(); decl.Cron != "0 * * * *" || decl.CatchUp != time.Hour {
		t.Errorf("Declaration reflected the override: %+v", decl)
	}
}

func TestSetCronScheduleLockPinsAndUnlocks(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	locked := cronBase.Add(time.Minute)
	lock := store.CronLock{Ref: "abc123", Binary: "/home/bin/nightly", Digest: "sha256:beef"}
	if err := st.SetCronScheduleLock(ctx, sched.ID, lock, locked); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LockedRef != lock.Ref || got.LockedBinary != lock.Binary || got.LockedDigest != lock.Digest {
		t.Errorf("lock = %+v, want %+v", got.Declaration(), lock)
	}
	if !got.UpdatedAt.Equal(locked) {
		t.Errorf("updated_at = %v, want %v", got.UpdatedAt, locked)
	}
	if err := st.SetCronScheduleLock(ctx, sched.ID, store.CronLock{}, locked.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, err = st.GetCronSchedule(ctx, sched.ID); err != nil {
		t.Fatal(err)
	}
	if got.LockedRef != "" || got.LockedBinary != "" || got.LockedDigest != "" {
		t.Errorf("a zero lock left %+v", got.Declaration())
	}
	if err := st.SetCronScheduleLock(ctx, "crn_absent", lock, locked); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("lock of an unknown id = %v, want ErrNotFound", err)
	}
}

func TestCronOverrideMutatorsReportAnUnknownID(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	for name, err := range map[string]error{
		"set":    st.SetCronOverride(ctx, "crn_absent", store.CronOverride{SetAt: cronBase}, cronBase),
		"clear":  st.ClearCronOverride(ctx, "crn_absent", cronBase),
		"delete": st.DeleteCronSchedule(ctx, "crn_absent"),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s = %v, want ErrNotFound", name, err)
		}
	}
}

func TestSetCronOverrideRefusesAnUnknownOverlap(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	if err := st.SetCronOverride(ctx, sched.ID, store.CronOverride{
		Overlap: "pile-up", SetAt: cronBase,
	}, cronBase); err == nil {
		t.Fatal("SetCronOverride accepted an overlap outside the enum")
	}
}

func TestDeleteCronScheduleTakesItsFiresAndSparesSiblings(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	var ids []string
	for _, name := range []string{"morning", "evening"} {
		sched, _, err := st.ArmCronSchedule(ctx, store.CronSchedule{
			ID: "crn_" + name, RepoPath: "/repo/one", Pipeline: "nightly", Name: name,
			Cron: "0 * * * *", TZ: "UTC", CatchUp: time.Hour,
		}, cronBase)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sched.ID)
		at := cronBase.Add(time.Minute)
		if err := st.ResolveCronDue(ctx, sched.ID, at, nil, &store.CronFire{
			DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeFired, RunID: "run-" + name,
		}, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteCronSchedule(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCronSchedule(ctx, ids[0]); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetCronSchedule after delete = %v, want ErrNotFound", err)
	}
	gone, err := st.ListCronFires(ctx, ids[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Errorf("delete left %d fires behind", len(gone))
	}
	if _, err := st.GetCronSchedule(ctx, ids[1]); err != nil {
		t.Errorf("delete took the sibling schedule: %v", err)
	}
	kept, err := st.ListCronFires(ctx, ids[1], 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Errorf("sibling fires = %d, want 1", len(kept))
	}
}

func TestCronFireRoundTripsArgs(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	sched := armCron(t, st, "crn_a", "/repo/one", "nightly", cronBase)
	at := cronBase.Add(time.Hour)
	args := map[string]string{"env": "prod", "shard": "2"}
	if err := st.ResolveCronDue(ctx, sched.ID, at, nil, &store.CronFire{
		DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeFired, RunID: "run-1", Args: args,
	}, at); err != nil {
		t.Fatal(err)
	}
	bare := at.Add(time.Hour)
	if err := st.ResolveCronDue(ctx, sched.ID, bare, nil, &store.CronFire{
		DueAt: bare, DecidedAt: bare, Outcome: store.CronOutcomeMissed,
	}, bare); err != nil {
		t.Fatal(err)
	}
	fires, err := st.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 2 {
		t.Fatalf("fires = %d, want 2", len(fires))
	}
	if fires[0].Args != nil {
		t.Errorf("a fire launched with no arguments read back %v", fires[0].Args)
	}
	if !maps.Equal(fires[1].Args, args) {
		t.Errorf("fire args = %v, want %v", fires[1].Args, args)
	}
}
