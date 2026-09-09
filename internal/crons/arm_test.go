package crons

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const twoSidedRepo = `  - name: sweep
    entrypoint: Sweep
    on:
      schedule:
        - name: quick
          cron: "*/15 * * * *"
          where: local
          args:
            depth: shallow
        - name: cluster
          cron: "0 4 * * *"
          where: controller
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "0 3 * * *"
        where: local
`

// safety: the arm copies whatever the compiler names, so a stub file exercises
// the pin without building Go.
type fakeCompiler struct {
	mu       sync.Mutex
	dir      string
	asked    []string
	failFor  string
	revision int
}

func newFakeCompiler(t *testing.T) *fakeCompiler {
	t.Helper()
	return &fakeCompiler{dir: t.TempDir()}
}

func (c *fakeCompiler) prove(_ context.Context, repoRoot, pipeline string) (Proof, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, pipeline)
	if c.failFor == pipeline {
		return Proof{}, fmt.Errorf("pipeline %q does not compile", pipeline)
	}
	path := filepath.Join(c.dir, filepath.Base(repoRoot)+"-"+pipeline)
	body := fmt.Sprintf("#!/bin/sh\necho %s r%d\n", pipeline, c.revision)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		return Proof{}, err
	}
	return Proof{Binary: path}, nil
}

func (c *fakeCompiler) proved() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

func gitRepo(t *testing.T, name, pipelinesBody string) string {
	t.Helper()
	root := writeRepo(t, name, pipelinesBody)
	git(t, root, "init", "--quiet")
	git(t, root, "config", "user.email", "tester@example.invalid")
	git(t, root, "config", "user.name", "tester")
	git(t, root, "add", ".")
	git(t, root, "commit", "--quiet", "-m", "declare the schedules")
	return root
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDeclaredSchedulesReadsEveryEntryWithItsNameWhereAndArgs(t *testing.T) {
	root := writeRepo(t, "svc", twoSidedRepo)
	declared, err := DeclaredSchedules(root)
	if err != nil {
		t.Fatalf("DeclaredSchedules: %v", err)
	}
	if len(declared) != 3 {
		t.Fatalf("declared %d entries, want 3: %+v", len(declared), declared)
	}
	quick := declared[0]
	if quick.Name != "quick" || quick.Selector() != "sweep/quick" || quick.DisplayName() != "svc/sweep/quick" {
		t.Errorf("first entry: %+v", quick)
	}
	if !quick.Local() || quick.Trigger.Args["depth"] != "shallow" {
		t.Errorf("first entry's where and args: %+v", quick.Trigger)
	}
	if declared[1].Local() || declared[1].Name != "cluster" {
		t.Errorf("second entry should fire from the controller: %+v", declared[1])
	}
	lone := declared[2]
	if lone.Name != store.CronScheduleDefaultName || lone.DisplayName() != "svc/nightly" {
		t.Errorf("a lone entry: %+v", lone)
	}
	if lone.ID() != ScheduleID(root, "nightly", "") {
		t.Errorf("id = %q", lone.ID())
	}
}

func TestArmPinsTheCompiledBinaryAndSkipsControllerEntries(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", twoSidedRepo)
	compiler := newFakeCompiler(t)

	report, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove})
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if report.Armed != 2 || len(report.Schedules) != 2 {
		t.Fatalf("armed: %+v", report)
	}
	if len(report.Controller) != 1 || report.Controller[0] != "svc/sweep/cluster" {
		t.Fatalf("controller entries: %+v", report.Controller)
	}
	if _, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "sweep", "cluster")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a controller entry was armed here: %v", err)
	}

	head := git(t, root, "rev-parse", "HEAD")
	quick, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "sweep", "quick"))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if quick.LockedRef != head {
		t.Errorf("locked ref = %q, want HEAD %q", quick.LockedRef, head)
	}
	wantBinary := filepath.Join(h.svc.PinRoot, quick.ID, PinnedBinaryName)
	if quick.LockedBinary != wantBinary {
		t.Errorf("locked binary = %q, want %q", quick.LockedBinary, wantBinary)
	}
	wantDigest, derr := FileDigest(wantBinary)
	if derr != nil {
		t.Fatalf("digest the pinned binary: %v", derr)
	}
	if quick.LockedDigest != wantDigest {
		t.Errorf("locked digest = %q, want the pinned file's own sha256 %q", quick.LockedDigest, wantDigest)
	}
	info, err := os.Stat(quick.LockedBinary)
	if err != nil {
		t.Fatalf("stat the pinned binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the pinned binary is not executable: %s", info.Mode())
	}
	dir, err := os.Stat(filepath.Dir(quick.LockedBinary))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Errorf("pin directory mode = %v (err %v), want 0700", dir.Mode().Perm(), err)
	}
	if quick.Args["depth"] != "shallow" || quick.Where != store.CronWhereLocal {
		t.Errorf("the entry's args and where were not stored: %+v", quick)
	}

	// safety: one compile serves every entry of a pipeline, so the second
	// entry of sweep must not compile it again.
	if proved := compiler.proved(); len(proved) != 2 {
		t.Errorf("proved %v, want one compile per pipeline", proved)
	}

	compiler.revision = 1
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("re-Arm: %v", err)
	}
	repinned, err := h.store.GetCronSchedule(ctx, quick.ID)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if repinned.LockedDigest == quick.LockedDigest {
		t.Errorf("re-arming did not re-pin: digest is still %q", repinned.LockedDigest)
	}
	if again, derr := FileDigest(repinned.LockedBinary); derr != nil || again != repinned.LockedDigest {
		t.Errorf("re-pinned digest = %q, want the new file's sha256 %q (err %v)",
			repinned.LockedDigest, again, derr)
	}
	body, err := os.ReadFile(repinned.LockedBinary)
	if err != nil || !strings.Contains(string(body), "r1") {
		t.Errorf("the pinned binary was not replaced: %q (err %v)", body, err)
	}
}

func TestArmOnlyRestrictsTheEntriesAndRefusesAnUnknownName(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", twoSidedRepo)

	_, err := h.svc.Arm(ctx, root, ArmOptions{Only: []string{"sweep/quick", "nope"}})
	if err == nil {
		t.Fatal("an unknown --only name armed the repo")
	}
	for _, want := range []string{"nope", "sweep/quick", "nightly/default"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	rows, err := h.store.ListCronSchedules(ctx)
	if err != nil {
		t.Fatalf("ListCronSchedules: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("an unknown name armed %d schedule(s)", len(rows))
	}

	report, err := h.svc.Arm(ctx, root, ArmOptions{Only: []string{"sweep/quick"}})
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if report.Armed != 1 || len(report.Schedules) != 1 {
		t.Fatalf("armed: %+v", report)
	}
	if report.Schedules[0].ID != ScheduleID(root, "sweep", "quick") {
		t.Fatalf("armed the wrong entry: %+v", report.Schedules[0])
	}
	if report.Withdrawn != 0 {
		t.Errorf("a subset arm withdrew %d schedule(s) it was not asked about", report.Withdrawn)
	}
}

func TestArmFollowLeavesTheScheduleOnTheCheckout(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	compiler := newFakeCompiler(t)

	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true, Prove: compiler.prove}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	sched, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "every-minute", ""))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if sched.LockedBinary != "" || sched.LockedRef != "" || sched.LockedDigest != "" {
		t.Fatalf("--follow pinned the schedule: %+v", sched)
	}
	if len(compiler.proved()) != 1 {
		t.Errorf("--follow skipped the proof: %v", compiler.proved())
	}
	rows, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].Lock.State != LockFollows || rows[0].StateDetail != "" {
		t.Errorf("lock: %+v", rows[0].Lock)
	}
}

func TestArmWithoutProofPinsNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	sched, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "every-minute", ""))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if sched.LockedBinary != "" {
		t.Fatalf("arming without a compile pinned %q", sched.LockedBinary)
	}
}

func TestDisarmRemovesOneScheduleAndItsPin(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", twoSidedRepo)
	compiler := newFakeCompiler(t)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	quick := ScheduleID(root, "sweep", "quick")
	pinDir := filepath.Join(h.svc.PinRoot, quick)
	if _, err := os.Stat(pinDir); err != nil {
		t.Fatalf("the arm wrote no pin: %v", err)
	}

	if err := h.svc.Disarm(ctx, quick); err != nil {
		t.Fatalf("Disarm: %v", err)
	}
	if _, err := os.Stat(pinDir); !os.IsNotExist(err) {
		t.Errorf("the pinned directory outlived the schedule: %v", err)
	}
	if _, err := h.store.GetCronSchedule(ctx, quick); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the row survived: %v", err)
	}
	if _, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "nightly", "")); err != nil {
		t.Errorf("disarming one entry took a sibling with it: %v", err)
	}
}

func TestLockAndUnlockMoveTheScheduleBetweenPinAndCheckout(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	compiler := newFakeCompiler(t)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")

	locked, err := h.svc.Lock(ctx, id, compiler.prove)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if locked.LockedRef != git(t, root, "rev-parse", "HEAD") || locked.LockedBinary == "" {
		t.Fatalf("Lock did not pin: %+v", locked)
	}
	if _, err := os.Stat(locked.LockedBinary); err != nil {
		t.Fatalf("stat the pinned binary: %v", err)
	}

	unlocked, err := h.svc.Unlock(ctx, id)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if unlocked.LockedRef != "" || unlocked.LockedBinary != "" || unlocked.LockedDigest != "" {
		t.Fatalf("Unlock left a pin: %+v", unlocked)
	}
	if _, err := os.Stat(filepath.Join(h.svc.PinRoot, id)); !os.IsNotExist(err) {
		t.Errorf("Unlock left the pinned directory behind: %v", err)
	}
}

func TestRefreshDerivesDriftAndLeavesALockedDeclarationAlone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	compiler := newFakeCompiler(t)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")

	rows, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].Lock.State != LockPinned {
		t.Fatalf("a freshly pinned schedule: %+v", rows[0].Lock)
	}
	if want := "locked " + rows[0].Lock.ShortRef(); rows[0].StateDetail != want {
		t.Errorf("state detail = %q, want %q", rows[0].StateDetail, want)
	}

	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+strings.Replace(everyMinute, `"* * * * *"`, `"0 4 * * *"`, 1)), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	report, err := h.svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if report.Updated != 0 || report.Locked != 1 {
		t.Fatalf("refresh read the working tree of a locked schedule: %+v", report)
	}
	stored, err := h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if stored.Cron != "* * * * *" {
		t.Errorf("the locked declaration moved to %q", stored.Cron)
	}
	if rows, err = h.svc.List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].Lock.State != LockDirty || rows[0].StateDetail != "locked, checkout dirty" {
		t.Fatalf("an edited working tree: %+v (%q)", rows[0].Lock, rows[0].StateDetail)
	}

	git(t, root, "add", ".")
	git(t, root, "commit", "--quiet", "-m", "move the cadence")
	if rows, err = h.svc.List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].Lock.State != LockAhead || rows[0].StateDetail != "locked, checkout ahead" {
		t.Fatalf("a checkout ahead of the pin: %+v (%q)", rows[0].Lock, rows[0].StateDetail)
	}

	if _, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("re-Arm: %v", err)
	}
	if rows, err = h.svc.List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].Lock.State != LockPinned || rows[0].Cron != "0 4 * * *" {
		t.Fatalf("re-arming did not re-pin the new commit: %+v", rows[0])
	}

	if err := os.Remove(rows[0].Lock.Binary); err != nil {
		t.Fatalf("remove the pinned binary: %v", err)
	}
	if rows, err = h.svc.List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].Lock.State != LockMissing || rows[0].StateDetail != "locked, binary missing" {
		t.Fatalf("a pin whose binary is gone: %+v (%q)", rows[0].Lock, rows[0].StateDetail)
	}
}

func TestRefreshFollowsTheCheckoutForAnUnlockedSchedule(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+strings.Replace(everyMinute, `"* * * * *"`, `"0 4 * * *"`, 1)), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	report, err := h.svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if report.Updated != 1 || report.Locked != 0 {
		t.Fatalf("refresh: %+v", report)
	}
	stored, err := h.store.GetCronSchedule(ctx, ScheduleID(root, "every-minute", ""))
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if stored.Cron != "0 4 * * *" {
		t.Errorf("an unlocked schedule did not follow the checkout: %q", stored.Cron)
	}
}

func TestOverrideRunsTheHostsValuesAndGoesStaleWhenTheRepoMoves(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")

	cron := "*/5 * * * *"
	catchUp := 6 * time.Hour
	row, err := h.svc.SetOverride(ctx, id, Override{
		Cron:    &cron,
		CatchUp: &catchUp,
		Args:    map[string]string{"depth": "deep"},
	})
	if err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	if row.Effective.Cron != cron || row.Effective.CatchUp != catchUp {
		t.Fatalf("effective cadence: %+v", row.Effective)
	}
	if row.Cron != "* * * * *" {
		t.Errorf("the declaration moved: %q", row.Cron)
	}
	if row.Effective.Args["depth"] != "deep" || row.OverrideStale {
		t.Errorf("override: %+v stale=%v", row.Effective.Args, row.OverrideStale)
	}
	if strings.Join(row.OverrideFields, ",") != "cron,catch_up,args" {
		t.Errorf("override fields = %v", row.OverrideFields)
	}

	tz := "America/Denver"
	if row, err = h.svc.SetOverride(ctx, id, Override{TZ: &tz}); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	if row.Effective.Cron != cron || row.Effective.TZ != tz {
		t.Fatalf("the second override dropped the first: %+v", row.Effective)
	}

	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+strings.Replace(everyMinute, `"* * * * *"`, `"0 2 * * *"`, 1)), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	if _, err := h.svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	rows, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !rows[0].OverrideStale {
		t.Fatal("an override set against a declaration that has moved is not stale")
	}
	if rows[0].Effective.Cron != cron {
		t.Errorf("a stale override stopped applying: %q", rows[0].Effective.Cron)
	}

	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("re-Arm: %v", err)
	}
	if rows, err = h.svc.List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].OverrideStale {
		t.Error("re-arming did not re-base the override")
	}
	if rows[0].Effective.Cron != cron {
		t.Errorf("re-arming dropped the override: %q", rows[0].Effective.Cron)
	}

	if row, err = h.svc.ClearOverride(ctx, id); err != nil {
		t.Fatalf("ClearOverride: %v", err)
	}
	if row.Effective.Cron != "0 2 * * *" || len(row.OverrideFields) != 0 {
		t.Fatalf("reset: %+v", row)
	}
}

func TestSetOverrideRefusesACadenceThatWouldNotEvaluate(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")
	bad := "not a cron"
	if _, err := h.svc.SetOverride(ctx, id, Override{Cron: &bad}); err == nil {
		t.Fatal("an unparseable cron was accepted")
	}
	stored, err := h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if stored.Override != nil {
		t.Fatalf("a refused override was stored: %+v", stored.Override)
	}
}

func TestTickFiresTheEffectiveCadenceAndRecordsItsArgs(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", `  - name: sweep
    entrypoint: Sweep
    on:
      schedule:
        cron: "0 3 * * *"
        where: local
        args:
          depth: shallow
`)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "sweep", "")
	cron := "* * * * *"
	if _, err := h.svc.SetOverride(ctx, id, Override{
		Cron: &cron,
		Args: map[string]string{"depth": "deep", "dry-run": "true"},
	}); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Fired != 1 {
		t.Fatalf("the overridden cadence did not fire: %+v", report)
	}
	if got := h.launcher.launches(); len(got) != 1 || got[0] != "sweep@2026-01-01T00:01:00Z" {
		t.Fatalf("launched %v", got)
	}
	if report.Decisions[0].Args["depth"] != "deep" {
		t.Errorf("the decision carries %v", report.Decisions[0].Args)
	}
	fires, err := h.store.ListCronFires(ctx, id, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 1 || fires[0].Args["depth"] != "deep" || fires[0].Args["dry-run"] != "true" {
		t.Fatalf("the fire did not record what it launched with: %+v", fires)
	}
}

func TestTickRecordsAnOverrideThatStoppedEvaluating(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")
	// safety: the store takes any zone string, so an override naming a zone
	// this host cannot load is what a tick has to survive.
	if err := h.store.SetCronOverride(ctx, id, store.CronOverride{TZ: "Mars/Olympus"}, h.clock.now()); err != nil {
		t.Fatalf("SetCronOverride: %v", err)
	}

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Failed != 1 || len(h.launcher.launches()) != 0 {
		t.Fatalf("tick: %+v, launches %v", report, h.launcher.launches())
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "Mars/Olympus") {
		t.Fatalf("errors = %v", report.Errors)
	}
	if !strings.Contains(report.Errors[0], "crons reset") {
		t.Errorf("the reason does not say how to drop the override: %q", report.Errors[0])
	}
	fires, err := h.store.ListCronFires(ctx, id, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 1 || fires[0].Outcome != store.CronOutcomeFailed {
		t.Fatalf("fires: %+v", fires)
	}

	// safety: a schedule that cannot evaluate is recorded once, not once a minute.
	h.clock.set(at(t, "2026-01-01T00:02:05Z"))
	if _, err := h.svc.Tick(ctx, false); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if fires, err = h.store.ListCronFires(ctx, id, 0); err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 1 {
		t.Fatalf("a repeating failure recorded %d fires", len(fires))
	}
}

func TestTickFailsAScheduleWhosePinnedBinaryIsGone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	compiler := newFakeCompiler(t)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")
	sched, err := h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if err := os.Remove(sched.LockedBinary); err != nil {
		t.Fatalf("remove the pinned binary: %v", err)
	}

	h.clock.set(at(t, "2026-01-01T00:01:05Z"))
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Failed != 1 || len(h.launcher.launches()) != 0 {
		t.Fatalf("tick: %+v, launches %v", report, h.launcher.launches())
	}
	fires, err := h.store.ListCronFires(ctx, id, 0)
	if err != nil {
		t.Fatalf("ListCronFires: %v", err)
	}
	if len(fires) != 1 || fires[0].Outcome != store.CronOutcomeFailed {
		t.Fatalf("fires: %+v", fires)
	}
	if !strings.Contains(fires[0].Detail, "crons install") {
		t.Errorf("the failure does not say how to fix it: %q", fires[0].Detail)
	}
	stored, err := h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetCronSchedule: %v", err)
	}
	if !stored.CursorAt.Equal(at(t, "2026-01-01T00:01:00Z")) {
		t.Errorf("cursor = %v; a failed instant still resolves", stored.CursorAt)
	}
}

func TestResolveTakesAnEntrySelectorAndNamesAmbiguousCandidates(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := writeRepo(t, "svc", twoSidedRepo)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	quick := ScheduleID(root, "sweep", "quick")
	for _, name := range []string{quick, "svc/sweep/quick", "sweep/quick", "sweep"} {
		got, err := h.svc.Resolve(ctx, name)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		if got.ID != quick {
			t.Errorf("Resolve(%q) = %s, want %s", name, got.ID, quick)
		}
	}

	other := writeRepo(t, "other", twoSidedRepo)
	if _, err := h.svc.Arm(ctx, other, ArmOptions{}); err != nil {
		t.Fatalf("Arm other: %v", err)
	}
	_, err := h.svc.Resolve(ctx, "sweep/quick")
	if !errors.Is(err, ErrAmbiguousName) {
		t.Fatalf("Resolve error = %v, want ErrAmbiguousName", err)
	}
	for _, want := range []string{"other/sweep/quick", "svc/sweep/quick"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestHealthCountsLocksFollowersAndStaleOverrides(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	pinned := gitRepo(t, "pinned", everyMinute)
	following := writeRepo(t, "following", everyMinute)
	compiler := newFakeCompiler(t)
	if _, err := h.svc.Arm(ctx, pinned, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("Arm pinned: %v", err)
	}
	if _, err := h.svc.Arm(ctx, following, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("Arm following: %v", err)
	}
	git(t, pinned, "commit", "--quiet", "--allow-empty", "-m", "move past the pin")

	cron := "*/5 * * * *"
	id := ScheduleID(following, "every-minute", "")
	if _, err := h.svc.SetOverride(ctx, id, Override{Cron: &cron}); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	if err := os.WriteFile(filepath.Join(following, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+strings.Replace(everyMinute, `"* * * * *"`, `"0 6 * * *"`, 1)), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	if _, err := h.svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	health, err := h.svc.Health(ctx, fakeTimerHost(t, h.home, true))
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Locked != 1 || health.Following != 1 || health.Ahead != 1 || health.StaleOverride != 1 {
		t.Fatalf("counts: %+v", health)
	}
	if !strings.Contains(health.Remedy, "crons install") {
		t.Errorf("remedy = %q", health.Remedy)
	}
}

func TestArmFollowDropsAnExistingPinnedBinary(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	compiler := newFakeCompiler(t)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")
	pinDir := filepath.Join(h.svc.PinRoot, id)
	if _, err := os.Stat(pinDir); err != nil {
		t.Fatalf("the first arm wrote no pin: %v", err)
	}

	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true, Prove: compiler.prove}); err != nil {
		t.Fatalf("re-Arm with --follow: %v", err)
	}
	stored, err := h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LockedBinary != "" {
		t.Errorf("--follow left the row pinned to %q", stored.LockedBinary)
	}
	if _, err := os.Stat(pinDir); !os.IsNotExist(err) {
		t.Errorf("--follow left the pinned binary behind: %v", err)
	}
}

func TestArmWithoutProofDropsAnExistingPinnedBinary(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	compiler := newFakeCompiler(t)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Prove: compiler.prove}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")
	if _, err := h.svc.Arm(ctx, root, ArmOptions{}); err != nil {
		t.Fatalf("re-Arm without proof: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.svc.PinRoot, id)); !os.IsNotExist(err) {
		t.Errorf("--no-prove left the pinned binary behind: %v", err)
	}
}

func TestRefreshKeepsTheLockOfARowItRepublishes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")
	// safety: only a pin the refresh does not skip can prove the lock is
	// carried through, and Refresh skips a row with a pinned binary, so the
	// row carries a ref alone -- the shape `crons install --no-prove` leaves.
	lock := store.CronLock{Ref: "0123456789abcdef0123456789abcdef01234567"}
	if err := h.store.SetCronScheduleLock(ctx, id, lock, h.clock.now()); err != nil {
		t.Fatalf("SetCronScheduleLock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+strings.Replace(everyMinute, `"* * * * *"`, `"0 4 * * *"`, 1)), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	report, err := h.svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if report.Updated != 1 {
		t.Fatalf("refresh: %+v", report)
	}
	stored, err := h.store.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Cron != "0 4 * * *" {
		t.Errorf("the declaration did not move: %q", stored.Cron)
	}
	if stored.LockedRef != lock.Ref {
		t.Errorf("refresh blanked the lock: ref = %q, want %q", stored.LockedRef, lock.Ref)
	}
}

func TestArmReportsTheOverridesItReBased(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")
	cron := "*/5 * * * *"
	if _, err := h.svc.SetOverride(ctx, id, Override{Cron: &cron}); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"),
		[]byte("pipelines:\n"+strings.Replace(everyMinute, `"* * * * *"`, `"0 4 * * *"`, 1)), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "--quiet", "-m", "move the cadence")

	report, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true})
	if err != nil {
		t.Fatalf("re-Arm: %v", err)
	}
	if len(report.Rebased) != 1 {
		t.Fatalf("report.Rebased = %+v, want the one override the arm moved", report.Rebased)
	}
	rb := report.Rebased[0]
	if rb.Schedule != id || rb.Name != "svc/every-minute" {
		t.Errorf("rebase names %q/%q, want the schedule it moved", rb.Schedule, rb.Name)
	}
	if rb.From.Cron != "* * * * *" || rb.To.Cron != "0 4 * * *" {
		t.Errorf("rebase = %+v, want the old and new declarations", rb)
	}

	// safety: the arm has already re-based it, so a second one has nothing to
	// report and must not repeat the warning.
	again, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true})
	if err != nil {
		t.Fatalf("third Arm: %v", err)
	}
	if len(again.Rebased) != 0 {
		t.Errorf("a settled override was reported re-based again: %+v", again.Rebased)
	}
}

func TestPushedRepoAcceptsEveryCloneURLFormAndNoLocalPath(t *testing.T) {
	for _, url := range []string{
		"https://github.com/acme/widgets.git",
		"ssh://git@github.com/acme/widgets.git",
		"git@github.com:acme/widgets.git",
		"deploy@git.example.com:acme/widgets.git",
		"ci-bot@git.internal.example:srv/widgets",
	} {
		if !PushedRepo(url) {
			t.Errorf("PushedRepo(%q) = false, want true", url)
		}
	}
	for _, path := range []string{
		"/home/korey/code/widgets", "./widgets", "widgets",
		"/tmp/a@b", "git@github.com", "-flag@host:path",
	} {
		if PushedRepo(path) {
			t.Errorf("PushedRepo(%q) = true, want false", path)
		}
	}
}

func TestTickLeavesTheOtherSidesSchedulesAlone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, at(t, "2026-01-01T00:00:30Z"))
	root := gitRepo(t, "svc", everyMinute)
	if _, err := h.svc.Arm(ctx, root, ArmOptions{Follow: true}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	id := ScheduleID(root, "every-minute", "")

	// safety: a controller row that reached this store fires from the
	// controller, never from the host's timer.
	if _, _, err := h.store.ArmCronSchedule(ctx, store.CronSchedule{
		ID: "crn_pushed", RepoPath: "https://github.com/acme/widgets.git", Pipeline: "nightly",
		Name: store.CronScheduleDefaultName, Cron: "* * * * *", TZ: "UTC",
		Overlap: store.CronOverlapSkip, CatchUp: time.Hour, Where: store.CronWhereController,
	}, h.clock.now()); err != nil {
		t.Fatalf("seed a controller row: %v", err)
	}

	h.clock.at = at(t, "2026-01-01T00:02:30Z")
	report, err := h.svc.Tick(ctx, false)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Evaluated != 1 {
		t.Errorf("the local tick evaluated %d schedules, want only its own", report.Evaluated)
	}
	for _, d := range report.Decisions {
		if d.Schedule.ID != id {
			t.Errorf("the local tick resolved %s, which fires from the controller", d.Schedule.ID)
		}
	}
	pushed, err := h.store.GetCronSchedule(ctx, "crn_pushed")
	if err != nil {
		t.Fatal(err)
	}
	if !pushed.CursorAt.IsZero() && pushed.LastOutcome != "" {
		t.Errorf("the local tick moved a controller row: %+v", pushed)
	}
}
