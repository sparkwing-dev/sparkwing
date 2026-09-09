package crons

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// fakeLauncher records what a tick asked for and answers the overlap question
// from a set of run ids the test declares still going.
type fakeLauncher struct {
	mu         sync.Mutex
	launched   []string
	runIDs     []string
	nextRun    int
	active     map[string]bool
	launchErr  error
	activeErr  error
	staleAfter []time.Duration
}

func newFakeLauncher(runIDs ...string) *fakeLauncher {
	return &fakeLauncher{runIDs: runIDs, active: map[string]bool{}}
}

func (f *fakeLauncher) Launch(_ context.Context, s store.CronSchedule, due time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.launchErr != nil {
		return "", f.launchErr
	}
	id := fmt.Sprintf("run-%d", f.nextRun+1)
	if f.nextRun < len(f.runIDs) {
		id = f.runIDs[f.nextRun]
	}
	f.nextRun++
	f.launched = append(f.launched, fmt.Sprintf("%s@%s", s.Pipeline, due.UTC().Format(time.RFC3339)))
	return id, nil
}

func (f *fakeLauncher) Active(_ context.Context, runID string, staleAfter time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staleAfter = append(f.staleAfter, staleAfter)
	if f.activeErr != nil {
		return false, f.activeErr
	}
	return f.active[runID], nil
}

func (f *fakeLauncher) launches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.launched...)
}

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

func at(t *testing.T, rfc string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatalf("parse %q: %v", rfc, err)
	}
	return parsed
}

type harness struct {
	svc      *Service
	store    *store.Store
	launcher *fakeLauncher
	clock    *clock
	home     string
}

func newHarness(t *testing.T, now time.Time) *harness {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	c := &clock{at: now}
	launcher := newFakeLauncher()
	return &harness{
		svc: &Service{
			Store:    st,
			Launcher: launcher,
			Now:      c.now,
			Host:     "test-host",
			Version:  "test",
			LockPath: filepath.Join(home, "crons.lock"),
			ArmedBy:  "tester@test-host",
		},
		store: st, launcher: launcher, clock: c, home: home,
	}
}

// writeRepo lays down a checkout whose sparkwing.yaml declares the given
// pipelines block body.
func writeRepo(t *testing.T, name, pipelinesBody string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	dir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "pipelines:\n" + pipelinesBody
	if err := os.WriteFile(filepath.Join(dir, "sparkwing.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return root
}

const everyMinute = `  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule: "* * * * *"
`

func TestScheduleIDIsStableAndScopedToTheCheckout(t *testing.T) {
	first := ScheduleID("/repos/alpha", "nightly")
	if first != ScheduleID("/repos/alpha", "nightly") {
		t.Fatal("ScheduleID is not stable for the same inputs")
	}
	if !strings.HasPrefix(first, ScheduleIDPrefix) || len(first) != len(ScheduleIDPrefix)+scheduleIDHexLen {
		t.Fatalf("ScheduleID shape %q", first)
	}
	if first == ScheduleID("/repos/beta", "nightly") {
		t.Fatal("two checkouts share a schedule id")
	}
	if first == ScheduleID("/repos/alpha", "weekly") {
		t.Fatal("two pipelines in one checkout share a schedule id")
	}
	// The NUL separator keeps the two fields from running together.
	if ScheduleID("/repos/a", "bc") == ScheduleID("/repos/ab", "c") {
		t.Fatal("the separator does not separate the fields")
	}
}

func TestDisplayNameIsRepoBaseAndPipeline(t *testing.T) {
	got := DisplayName(store.CronSchedule{RepoPath: "/repos/alpha", Pipeline: "nightly"})
	if got != "alpha/nightly" {
		t.Fatalf("DisplayName = %q", got)
	}
}

func TestDeclaredSchedulesReadsBothYAMLShapes(t *testing.T) {
	root := writeRepo(t, "svc", `  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 3 * * *"
  - name: hourly
    entrypoint: Hourly
    on:
      schedule:
        cron: "@hourly"
        tz: America/Denver
        overlap: queue
        catch_up: 6h
  - name: manual
    entrypoint: Manual
`)
	declared, err := DeclaredSchedules(root)
	if err != nil {
		t.Fatalf("DeclaredSchedules: %v", err)
	}
	if len(declared) != 2 {
		t.Fatalf("declared %d schedules, want 2: %+v", len(declared), declared)
	}
	if declared[0].Pipeline != "nightly" || declared[0].Trigger.Cron != "0 3 * * *" {
		t.Errorf("scalar form: %+v", declared[0])
	}
	if declared[0].Trigger.TZ != "" || declared[0].Trigger.OverlapPolicy() != store.CronOverlapSkip {
		t.Errorf("scalar form should carry the defaults: %+v", declared[0].Trigger)
	}
	m := declared[1].Trigger
	if m.Cron != "@hourly" || m.TZ != "America/Denver" || m.Overlap != "queue" || m.CatchUp != "6h" {
		t.Errorf("mapping form: %+v", m)
	}
	if declared[1].RepoPath != root {
		t.Errorf("RepoPath = %q, want %q", declared[1].RepoPath, root)
	}
}

func TestDeclaredSchedulesRefusesToGuessAtAnUnreadableCheckout(t *testing.T) {
	if _, err := DeclaredSchedules(t.TempDir()); err == nil {
		t.Fatal("a checkout with no config read as declaring nothing")
	}
	if _, err := DeclaredSchedules(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("a missing checkout read as declaring nothing")
	}
}

func TestDeclaredSchedulesOnAConfigWithNoCadence(t *testing.T) {
	declared, err := DeclaredSchedules(writeRepo(t, "svc", `  - name: manual
    entrypoint: Manual
`))
	if err != nil {
		t.Fatalf("DeclaredSchedules: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared %d schedules, want none", len(declared))
	}
}

func TestArmCreatesThenRefreshesWithoutClobberingState(t *testing.T) {
	ctx := context.Background()
	now := at(t, "2026-01-01T00:00:30Z")
	h := newHarness(t, now)
	root := writeRepo(t, "svc", everyMinute)

	report, err := h.svc.Arm(ctx, root, nil)
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if report.Armed != 1 || report.Refreshed != 0 || report.Withdrawn != 0 {
		t.Fatalf("first arm: %+v", report)
	}
	id := ScheduleID(root, "every-minute")
	sched, err := h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if sched.ArmedBy != "tester@test-host" || !sched.Declared || sched.Paused {
		t.Fatalf("armed row: %+v", sched)
	}
	if sched.NextDueAt == nil || !sched.NextDueAt.Equal(at(t, "2026-01-01T00:01:00Z")) {
		t.Fatalf("next due %v", sched.NextDueAt)
	}
	if !sched.CursorAt.Equal(now) {
		t.Fatalf("cursor = %v, want the arming instant", sched.CursorAt)
	}

	if err := h.svc.Pause(ctx, id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	h.clock.set(at(t, "2026-01-01T02:00:30Z"))
	cursor := at(t, "2026-01-01T01:59:00Z")
	if err := h.store.ResolveCronDue(ctx, id, cursor, nil, nil, h.clock.now()); err != nil {
		t.Fatalf("ResolveCronDue: %v", err)
	}

	// Re-arming republishes the declaration; the pause and cursor are host state.
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(`pipelines:
  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule:
        cron: "*/5 * * * *"
        tz: America/Denver
        overlap: queue
`), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	report, err = h.svc.Arm(ctx, root, nil)
	if err != nil {
		t.Fatalf("re-Arm: %v", err)
	}
	if report.Armed != 0 || report.Refreshed != 1 {
		t.Fatalf("re-arm: %+v", report)
	}
	sched, err = h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if sched.Cron != "*/5 * * * *" || sched.TZ != "America/Denver" || sched.Overlap != store.CronOverlapQueue {
		t.Errorf("declaration not republished: %+v", sched)
	}
	if !sched.Paused {
		t.Error("re-arming cleared the pause")
	}
	if !sched.CursorAt.Equal(cursor) {
		t.Errorf("re-arming moved the cursor to %v", sched.CursorAt)
	}
}

func TestArmWithdrawsASchedulTheRepoStoppedDeclaring(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute+`  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 3 * * *"
`)
	if _, err := h.svc.Arm(ctx, root, nil); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+everyMinute), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	report, err := h.svc.Arm(ctx, root, nil)
	if err != nil {
		t.Fatalf("re-Arm: %v", err)
	}
	if report.Withdrawn != 1 || len(report.Withdrawals) != 1 || report.Withdrawals[0] != "svc/nightly" {
		t.Fatalf("withdrawal: %+v", report)
	}
	sched, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "nightly"))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if sched.Declared {
		t.Error("the withdrawn row is still declared")
	}
}

func TestArmWritesNothingWhenTheProofFails(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute)
	proofErr := errors.New("does not compile")
	_, err := h.svc.Arm(ctx, root, func(string, string) error { return proofErr })
	if !errors.Is(err, proofErr) {
		t.Fatalf("Arm error = %v, want the proof's", err)
	}
	rows, err := h.store.ListCronSchedules(ctx)
	if err != nil {
		t.Fatalf("ListCronSchedules: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a failed proof armed %d schedule(s)", len(rows))
	}
}

func TestDisarmRemovesEverySchedulOfTheRepo(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute+`  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 3 * * *"
`)
	other := writeRepo(t, "other", everyMinute)
	if _, err := h.svc.Arm(ctx, root, nil); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if _, err := h.svc.Arm(ctx, other, nil); err != nil {
		t.Fatalf("Arm other: %v", err)
	}
	removed, err := h.svc.Disarm(ctx, root)
	if err != nil {
		t.Fatalf("Disarm: %v", err)
	}
	if removed != 2 {
		t.Fatalf("Disarm removed %d, want 2", removed)
	}
	rows, err := h.store.ListCronSchedules(ctx)
	if err != nil {
		t.Fatalf("ListCronSchedules: %v", err)
	}
	if len(rows) != 1 || rows[0].RepoPath != other {
		t.Fatalf("Disarm touched another repo: %+v", rows)
	}
}

func TestRefreshUpdatesWithdrawsAndReportsAMissingCheckout(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	edited := writeRepo(t, "edited", everyMinute)
	dropped := writeRepo(t, "dropped", everyMinute+`  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 3 * * *"
`)
	gone := writeRepo(t, "gone", everyMinute)
	for _, root := range []string{edited, dropped, gone} {
		if _, err := h.svc.Arm(ctx, root, nil); err != nil {
			t.Fatalf("Arm %s: %v", root, err)
		}
	}

	if err := os.WriteFile(filepath.Join(edited, ".sparkwing", "sparkwing.yaml"), []byte(`pipelines:
  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule: "0 4 * * *"
`), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dropped, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+everyMinute), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove %s: %v", gone, err)
	}

	report, err := h.svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if report.Repos != 3 {
		t.Errorf("Repos = %d, want 3", report.Repos)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], gone) {
		t.Errorf("Refresh errors = %v, want one naming %s", report.Errors, gone)
	}
	if report.Withdrawn != 1 {
		t.Errorf("Withdrawn = %d, want only the dropped pipeline", report.Withdrawn)
	}
	changed, err := h.store.GetCronSchedule(ctx, ScheduleID(edited, "every-minute"))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if changed.Cron != "0 4 * * *" {
		t.Errorf("edited cron = %q", changed.Cron)
	}
	vanished, err := h.store.GetCronSchedule(ctx, ScheduleID(gone, "every-minute"))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !vanished.Declared {
		t.Error("a checkout that could not be read disarmed itself")
	}
}

func TestRefreshWritesNothingWhenTheDeclarationIsUnchanged(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute)
	armed := armOne(t, h, root)

	h.clock.set(at(t, "2026-01-01T00:01:30Z"))
	report, err := h.svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if report.Updated != 0 {
		t.Errorf("Updated = %d, want none: nothing in the repo moved", report.Updated)
	}
	stored, err := h.store.GetCronSchedule(ctx, armed.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !stored.UpdatedAt.Equal(armed.UpdatedAt) {
		t.Errorf("updated_at moved to %v on an unchanged repo", stored.UpdatedAt)
	}

	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(`pipelines:
  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule: "0 4 * * *"
`), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	report, err = h.svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if report.Updated != 1 {
		t.Fatalf("Updated = %d, want the edited cadence stored", report.Updated)
	}
	if stored, err = h.store.GetCronSchedule(ctx, armed.ID); err != nil || stored.Cron != "0 4 * * *" {
		t.Fatalf("edited row = %+v (err %v)", stored, err)
	}
}

func armOne(t *testing.T, h *harness, root string) store.CronSchedule {
	t.Helper()
	ctx := context.Background()
	if _, err := h.svc.Arm(ctx, root, nil); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	sched, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "every-minute"))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	return sched
}

func TestTickWithNothingDue(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	armOne(t, h, writeRepo(t, "svc", `  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule: "0 3 * * *"
`))
	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Evaluated != 1 || report.Fired != 0 || len(report.Decisions) != 0 {
		t.Fatalf("tick: %+v", report)
	}
	if len(h.launcher.launches()) != 0 {
		t.Fatalf("launched %v", h.launcher.launches())
	}
}

func TestTickFiresADueScheduleAndRecordsIt(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", everyMinute))
	h.clock.set(at(t, "2026-01-01T00:01:05Z"))

	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Evaluated != 1 || report.Fired != 1 {
		t.Fatalf("tick: %+v", report)
	}
	if got := h.launcher.launches(); len(got) != 1 || got[0] != "every-minute@2026-01-01T00:01:00Z" {
		t.Fatalf("launched %v", got)
	}
	stored, err := h.store.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !stored.CursorAt.Equal(at(t, "2026-01-01T00:01:00Z")) {
		t.Errorf("cursor = %v", stored.CursorAt)
	}
	if stored.LastOutcome != store.CronOutcomeFired || stored.LastRunID != "run-1" {
		t.Errorf("last fire: %+v", stored)
	}
	if stored.NextDueAt == nil || !stored.NextDueAt.Equal(at(t, "2026-01-01T00:02:00Z")) {
		t.Errorf("next due = %v", stored.NextDueAt)
	}
	fires, err := h.store.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 1 || fires[0].Outcome != store.CronOutcomeFired || fires[0].RunID != "run-1" {
		t.Fatalf("fires: %+v", fires)
	}
	tick, err := h.store.GetCronTick(ctx)
	if err != nil {
		t.Fatalf("GetCronTick: %v", err)
	}
	if !tick.At.Equal(h.clock.now()) || tick.Host != "test-host" || tick.Error != "" {
		t.Fatalf("tick record: %+v", tick)
	}
}

func TestTickSkipsWhenThePreviousRunIsStillGoing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", everyMinute))

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	if _, err := h.svc.Tick(ctx, false); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	h.launcher.mu.Lock()
	h.launcher.active["run-1"] = true
	h.launcher.mu.Unlock()

	h.clock.set(at(t, "2026-01-01T00:02:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if report.Fired != 0 || report.Skipped != 1 {
		t.Fatalf("tick: %+v", report)
	}
	if len(h.launcher.launches()) != 1 {
		t.Fatalf("a skipped instant launched: %v", h.launcher.launches())
	}
	stored, err := h.store.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !stored.CursorAt.Equal(at(t, "2026-01-01T00:02:00Z")) {
		t.Errorf("a skip did not move the cursor: %v", stored.CursorAt)
	}
	if stored.LastOutcome != store.CronOutcomeSkippedOverlap || stored.LastRunID != "run-1" {
		t.Errorf("a skip should keep the last successful launch: %+v", stored)
	}
}

func TestTickAsksActiveWithTheSchedulesCatchUpWindow(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	armOne(t, h, writeRepo(t, "svc", `  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule:
        cron: "* * * * *"
        catch_up: 15m
`))
	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	if _, err := h.svc.Tick(ctx, false); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	h.clock.set(at(t, "2026-01-01T00:02:05Z"))
	if _, err := h.svc.Tick(ctx, false); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	h.launcher.mu.Lock()
	defer h.launcher.mu.Unlock()
	if len(h.launcher.staleAfter) != 1 || h.launcher.staleAfter[0] != 15*time.Minute {
		t.Fatalf("Active was given %v, want the schedule's 15m catch-up", h.launcher.staleAfter)
	}
}

func TestTickQueuesOverAnActiveRunWhenThePolicySaysSo(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	armOne(t, h, writeRepo(t, "svc", `  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule:
        cron: "* * * * *"
        overlap: queue
`))
	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	if _, err := h.svc.Tick(ctx, false); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	h.launcher.mu.Lock()
	h.launcher.active["run-1"] = true
	h.launcher.mu.Unlock()

	h.clock.set(at(t, "2026-01-01T00:02:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if report.Fired != 1 || report.Skipped != 0 {
		t.Fatalf("queue policy: %+v", report)
	}
	if len(h.launcher.launches()) != 2 {
		t.Fatalf("launches: %v", h.launcher.launches())
	}
}

func TestTickRecordsACatchUpBacklogAsOneMissAndAdvancesTheCursor(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", `  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule:
        cron: "* * * * *"
        catch_up: 2m
`))
	// The host slept for an hour: only the newest instant is inside the window.
	h.clock.set(at(t, "2026-01-01T01:00:20Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Missed != 1 || report.Fired != 1 {
		t.Fatalf("catch-up: %+v", report)
	}
	fires, err := h.store.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 2 {
		t.Fatalf("fires: %+v", fires)
	}
	var missed *store.CronFire
	for i := range fires {
		if fires[i].Outcome == store.CronOutcomeMissed {
			missed = &fires[i]
		}
	}
	if missed == nil {
		t.Fatalf("no missed row: %+v", fires)
	}
	if !strings.Contains(missed.Detail, "due instants missed") {
		t.Errorf("missed detail = %q", missed.Detail)
	}
	if !missed.DueAt.Equal(at(t, "2026-01-01T00:59:00Z")) {
		t.Errorf("the missed row should carry the newest missed instant, got %v", missed.DueAt)
	}
	stored, err := h.store.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !stored.CursorAt.Equal(at(t, "2026-01-01T01:00:00Z")) {
		t.Errorf("cursor = %v, want the fired instant", stored.CursorAt)
	}
	if got := h.launcher.launches(); len(got) != 1 || got[0] != "every-minute@2026-01-01T01:00:00Z" {
		t.Fatalf("launched %v", got)
	}
}

func TestTickAdvancesAPausedScheduleWithoutFiring(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", everyMinute))
	if err := h.svc.Pause(ctx, sched.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	h.clock.set(at(t, "2026-01-01T00:05:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Evaluated != 0 || report.Fired != 0 {
		t.Fatalf("a paused schedule was evaluated: %+v", report)
	}
	if len(h.launcher.launches()) != 0 {
		t.Fatalf("a paused schedule launched: %v", h.launcher.launches())
	}
	stored, err := h.store.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if stored.NextDueAt == nil || !stored.NextDueAt.Equal(at(t, "2026-01-01T00:06:00Z")) {
		t.Fatalf("next due = %v", stored.NextDueAt)
	}
	if !stored.CursorAt.Equal(at(t, "2026-01-01T00:05:00Z")) {
		t.Fatalf("cursor = %v, want the latest instant that came due", stored.CursorAt)
	}
	fires, err := h.store.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 0 {
		t.Fatalf("a paused schedule recorded %+v", fires)
	}
}

func TestResumeDoesNotReplayThePauseWindow(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", everyMinute))
	if err := h.svc.Pause(ctx, sched.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// safety: no tick lands during the pause, so only Resume can move the cursor.
	h.clock.set(at(t, "2026-01-08T00:00:30Z"))
	if err := h.svc.Resume(ctx, sched.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	h.clock.set(at(t, "2026-01-08T00:01:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Missed != 0 {
		t.Fatalf("resuming replayed the week it was paused: %+v", report)
	}
	if report.Fired != 1 || len(h.launcher.launches()) != 1 {
		t.Fatalf("tick: %+v, launches %v", report, h.launcher.launches())
	}
	fires, err := h.store.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 1 || fires[0].Outcome != store.CronOutcomeFired {
		t.Fatalf("fires: %+v", fires)
	}
}

func TestTickNeverFiresAnUndeclaredSchedule(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute)
	sched := armOne(t, h, root)
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines: []\n"), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Evaluated != 0 || report.Fired != 0 {
		t.Fatalf("an undeclared schedule fired: %+v", report)
	}
	stored, err := h.store.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if stored.Declared {
		t.Error("the tick's refresh did not withdraw the row")
	}
}

func TestTickRecordsALauncherFailureAndKeepsGoing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	broken := armOne(t, h, writeRepo(t, "broken", everyMinute))
	healthyRoot := writeRepo(t, "healthy", everyMinute)
	if _, err := h.svc.Arm(ctx, healthyRoot, nil); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	healthy := ScheduleID(healthyRoot, "every-minute")

	// The first schedule the tick reaches fails; the second must still fire.
	first, second := broken.ID, healthy
	if broken.RepoPath > healthyRoot {
		first, second = healthy, broken.ID
	}
	h.launcher.mu.Lock()
	h.launcher.launchErr = errors.New("consumer will not start")
	h.launcher.mu.Unlock()

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Failed != 2 {
		t.Fatalf("both launches failed, report: %+v", report)
	}
	for _, id := range []string{first, second} {
		stored, gerr := h.store.GetCronSchedule(ctx, id)
		if gerr != nil {
			t.Fatalf("GetCronSchedule: %v", gerr)
		}
		if stored.LastOutcome != store.CronOutcomeFailed {
			t.Errorf("%s last outcome = %q", id, stored.LastOutcome)
		}
		if !stored.CursorAt.Equal(at(t, "2026-01-01T00:01:00Z")) {
			t.Errorf("%s cursor = %v; a failure must still move it", id, stored.CursorAt)
		}
		fires, ferr := h.store.ListCronFires(ctx, id, 0)
		if ferr != nil {
			t.Fatalf("ListCronFires: %v", ferr)
		}
		if len(fires) != 1 || fires[0].Detail != "consumer will not start" {
			t.Errorf("%s fires: %+v", id, fires)
		}
	}
}

func TestASecondTickIsRefusedWhileTheFirstHoldsTheLock(t *testing.T) {
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	unlock, err := lockTick(h.svc.LockPath)
	if err != nil {
		t.Fatalf("lockTick: %v", err)
	}
	defer unlock()
	if _, err := h.svc.Tick(context.Background(), false); !errors.Is(err, ErrTickRunning) {
		t.Fatalf("Tick error = %v, want ErrTickRunning", err)
	}
}

func TestDryRunEvaluatesAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", everyMinute))
	h.clock.set(at(t, "2026-01-01T00:01:05Z"))

	report, err := h.svc.Tick(ctx, true)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Fired != 1 || len(report.Decisions) != 1 {
		t.Fatalf("dry run did not report the fire: %+v", report)
	}
	if len(h.launcher.launches()) != 0 {
		t.Fatalf("a dry run launched %v", h.launcher.launches())
	}
	stored, err := h.store.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !stored.CursorAt.Equal(sched.CursorAt) {
		t.Errorf("a dry run moved the cursor to %v", stored.CursorAt)
	}
	fires, err := h.store.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 0 {
		t.Errorf("a dry run recorded %d fire(s)", len(fires))
	}
	tick, err := h.store.GetCronTick(ctx)
	if err != nil {
		t.Fatalf("GetCronTick: %v", err)
	}
	if !tick.At.IsZero() {
		t.Errorf("a dry run recorded a tick at %v", tick.At)
	}
}

func TestResolveByIDDisplayNameAndBarePipeline(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute+`  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 3 * * *"
`)
	sched := armOne(t, h, root)

	for _, name := range []string{sched.ID, "svc/every-minute", "every-minute"} {
		got, err := h.svc.Resolve(ctx, name)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		if got.ID != sched.ID {
			t.Errorf("Resolve(%q) = %s, want %s", name, got.ID, sched.ID)
		}
	}
	if _, err := h.svc.Resolve(ctx, "nope"); err == nil {
		t.Fatal("Resolve of an unknown name succeeded")
	}
}

func TestResolveNamesTheCandidatesWhenABareNameIsAmbiguous(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	for _, name := range []string{"alpha", "beta"} {
		if _, err := h.svc.Arm(ctx, writeRepo(t, name, everyMinute), nil); err != nil {
			t.Fatalf("Arm %s: %v", name, err)
		}
	}
	_, err := h.svc.Resolve(ctx, "every-minute")
	if !errors.Is(err, ErrAmbiguousName) {
		t.Fatalf("Resolve error = %v, want ErrAmbiguousName", err)
	}
	for _, want := range []string{"alpha/every-minute", "beta/every-minute"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if got, rerr := h.svc.Resolve(ctx, "alpha/every-minute"); rerr != nil || got.Pipeline != "every-minute" {
		t.Fatalf("Resolve of the display name: %v %+v", rerr, got)
	}
}

func TestRunNowLaunchesWithoutMovingTheCursor(t *testing.T) {
	ctx := context.Background()
	now := at(t, "2026-01-01T00:00:30Z")
	h := newHarness(t, now)
	sched := armOne(t, h, writeRepo(t, "svc", everyMinute))
	if err := h.svc.Pause(ctx, sched.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	h.clock.set(at(t, "2026-01-01T00:00:45Z"))

	runID, err := h.svc.RunNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if runID != "run-1" {
		t.Fatalf("run id = %q", runID)
	}
	stored, err := h.store.GetCronSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !stored.CursorAt.Equal(sched.CursorAt) {
		t.Errorf("RunNow moved the cursor to %v", stored.CursorAt)
	}
	if stored.LastRunID != runID || stored.LastOutcome != store.CronOutcomeFired {
		t.Errorf("RunNow did not stamp the row: %+v", stored)
	}
	fires, err := h.store.ListCronFires(ctx, sched.ID, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 1 || fires[0].Detail != "run now" {
		t.Fatalf("fires: %+v", fires)
	}
}

func TestUpcomingReadsTheScheduleInItsOwnZone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", `  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule:
        cron: "0 3 * * *"
        tz: America/Denver
`))
	got, err := h.svc.Upcoming(ctx, sched.ID, 2)
	if err != nil {
		t.Fatalf("Upcoming: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Upcoming returned %d instants", len(got))
	}
	// 03:00 in America/Denver is 10:00 UTC in January.
	if !got[0].Equal(at(t, "2026-01-01T10:00:00Z")) {
		t.Errorf("first instant = %v", got[0].UTC())
	}
	if !got[1].Equal(at(t, "2026-01-02T10:00:00Z")) {
		t.Errorf("second instant = %v", got[1].UTC())
	}
}

func TestListAndShowDeriveNameStateAndZone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	sched := armOne(t, h, writeRepo(t, "svc", everyMinute))
	rows, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "svc/every-minute" || rows[0].State != StateArmed {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].Location != time.UTC {
		t.Errorf("location = %v, want UTC", rows[0].Location)
	}
	if err := h.svc.Pause(ctx, sched.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	row, fires, err := h.svc.Show(ctx, sched.ID, 5)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if row.State != StatePaused || len(fires) != 0 {
		t.Fatalf("show: %+v %+v", row, fires)
	}
	if err := h.svc.Resume(ctx, sched.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if row, _, err = h.svc.Show(ctx, sched.ID, 5); err != nil || row.State != StateArmed {
		t.Fatalf("resume: %v %+v", err, row)
	}
}

func TestHealthReportsAStaleTick(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	armOne(t, h, writeRepo(t, "svc", everyMinute))

	timer := fakeTimerHost(t, h.home, true)
	fresh, err := h.svc.Health(ctx, timer)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !fresh.Timer.Enabled {
		t.Fatalf("timer: %+v", fresh.Timer)
	}
	if !fresh.TickStale {
		t.Error("a home that has never ticked with an enabled timer is stale")
	}

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	if _, err := h.svc.Tick(ctx, false); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	ticked, err := h.svc.Health(ctx, timer)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if ticked.TickStale || !ticked.Healthy() {
		t.Fatalf("a home that just ticked: %+v", ticked)
	}
	if ticked.Armed != 1 || ticked.Schedules != 1 {
		t.Errorf("counts: %+v", ticked)
	}

	h.clock.set(at(t, "2026-01-01T00:10:00Z"))
	stale, err := h.svc.Health(ctx, timer)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !stale.TickStale || stale.Healthy() {
		t.Fatalf("ten minutes without a tick: %+v", stale)
	}
	if !strings.Contains(stale.Detail, "crons.log") {
		t.Errorf("detail = %q, want the log named", stale.Detail)
	}
}

func TestHealthIsUnhealthyWhenTheLastTickReportedAnError(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	armOne(t, h, writeRepo(t, "svc", everyMinute))
	timer := fakeTimerHost(t, h.home, true)

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	if err := h.store.RecordCronTick(ctx, store.CronTick{
		At: h.clock.now(), Host: "test-host", Error: "svc/every-minute: consumer will not start",
	}); err != nil {
		t.Fatalf("RecordCronTick: %v", err)
	}
	health, err := h.svc.Health(ctx, timer)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.TickStale {
		t.Fatalf("the tick just landed: %+v", health)
	}
	if health.Healthy() {
		t.Fatal("a tick that reported a failure read as healthy")
	}
	if !strings.Contains(health.Detail, "consumer will not start") {
		t.Errorf("detail = %q, want the tick's own reason", health.Detail)
	}
}

// fakeTimerHost describes a machine whose service manager always answers, with
// the timer's files under a temporary config home.
func fakeTimerHost(t *testing.T, home string, installed bool) crontimer.Host {
	t.Helper()
	root := t.TempDir()
	h := crontimer.Host{
		GOOS:       "linux",
		Home:       root,
		ConfigHome: filepath.Join(root, ".config"),
		Binary:     "/usr/local/bin/sparkwing",
		PathEnv:    "/usr/bin:/bin",
		Env:        map[string]string{"SPARKWING_HOME": home},
		LogPath:    filepath.Join(home, "crons.log"),
		UID:        1000,
		Exec:       func(string, ...string) (string, error) { return "", nil },
	}
	if installed {
		if _, err := crontimer.Install(h); err != nil {
			t.Fatalf("install the timer fixture: %v", err)
		}
	}
	return h
}

func TestHealthAcceptsAHostTickingFromItsOwnScheduler(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	armOne(t, h, writeRepo(t, "svc", everyMinute))
	noTimer := fakeTimerHost(t, h.home, false)

	before, err := h.svc.Health(ctx, noTimer)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if before.Healthy() {
		t.Fatalf("an armed host with no timer and no tick: %+v", before)
	}

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	if _, err := h.svc.Tick(ctx, false); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	ticked, err := h.svc.Health(ctx, noTimer)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !ticked.Healthy() || ticked.TickStale {
		t.Fatalf("a host ticking from its own scheduler: %+v", ticked)
	}

	h.clock.set(at(t, "2026-01-01T01:00:00Z"))
	lapsed, err := h.svc.Health(ctx, noTimer)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !lapsed.TickStale || lapsed.Healthy() {
		t.Fatalf("an hour after the last external tick: %+v", lapsed)
	}
}
