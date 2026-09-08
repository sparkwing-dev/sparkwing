package store_test

import (
	"context"
	"errors"
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
		"delete":   st.DeleteCronSchedule(ctx, "crn_absent"),
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

func TestDeleteCronScheduleTakesItsFires(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	kept := armCron(t, st, "crn_keep", "/repo/two", "build", cronBase)
	doomed := armCron(t, st, "crn_gone", "/repo/one", "nightly", cronBase)
	for _, id := range []string{kept.ID, doomed.ID} {
		at := cronBase.Add(time.Minute)
		if err := st.ResolveCronDue(ctx, id, at, nil, &store.CronFire{
			DueAt: at, DecidedAt: at, Outcome: store.CronOutcomeFired, RunID: "run-" + id,
		}, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteCronSchedule(ctx, doomed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCronSchedule(ctx, doomed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted schedule still readable: %v", err)
	}
	fires, err := st.ListCronFires(ctx, doomed.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 0 {
		t.Errorf("deleted schedule kept %d fires", len(fires))
	}
	if fires, err = st.ListCronFires(ctx, kept.ID, 0); err != nil || len(fires) != 1 {
		t.Fatalf("sibling fires = %d (err %v), want 1", len(fires), err)
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
