package crons_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const pushedRepoURL = "https://github.com/acme/widgets.git"

func pushedService(t *testing.T) *crons.Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &crons.Service{Store: st, ArmedBy: "operator@laptop"}
}

func declaredEntry(pipeline, name, cron string) crons.Declared {
	return crons.Declared{
		Pipeline: pipeline,
		Name:     name,
		Trigger: pipelines.ScheduleTrigger{
			Name: name, Cron: cron, Where: pipelines.ScheduleWhereController,
		},
	}
}

func TestArmPushed_StoresCloneURLBranchAndPin(t *testing.T) {
	svc := pushedService(t)
	ctx := context.Background()

	report, err := svc.ArmPushed(ctx, crons.ArmPush{
		RepoURL: pushedRepoURL,
		Branch:  "main",
		SHA:     "0123456789abcdef0123456789abcdef01234567",
		Entries: []crons.Declared{declaredEntry("nightly", "default", "0 3 * * *")},
	})
	if err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	if report.Armed != 1 || report.Refreshed != 0 {
		t.Fatalf("report = %+v, want one armed", report)
	}
	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want one", len(rows))
	}
	row := rows[0]
	if row.RepoPath != pushedRepoURL {
		t.Errorf("repo_path = %q, want the clone URL", row.RepoPath)
	}
	if row.Where != store.CronWhereController {
		t.Errorf("where = %q, want controller", row.Where)
	}
	if row.GitBranch != "main" {
		t.Errorf("git_branch = %q, want main", row.GitBranch)
	}
	if row.LockedRef == "" || row.LockedBinary != "" {
		t.Errorf("lock = %+v, want a ref and no binary", row.Lock)
	}
	if row.Display != "acme/widgets/nightly" {
		t.Errorf("display = %q, want owner/name/pipeline", row.Display)
	}
	if row.StateDetail != "pinned 0123456" {
		t.Errorf("state detail = %q, want the short pinned ref", row.StateDetail)
	}
	if row.NextDueAt == nil {
		t.Error("a freshly pushed schedule has no next due instant")
	}
}

func TestArmPushed_FollowingTheBranchTipHasNoPin(t *testing.T) {
	svc := pushedService(t)
	ctx := context.Background()

	if _, err := svc.ArmPushed(ctx, crons.ArmPush{
		RepoURL: pushedRepoURL,
		Branch:  "main",
		Entries: []crons.Declared{declaredEntry("nightly", "default", "0 3 * * *")},
	}); err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if rows[0].LockedRef != "" {
		t.Errorf("locked_ref = %q, want empty while following the tip", rows[0].LockedRef)
	}
	if rows[0].StateDetail != "branch tip" {
		t.Errorf("state detail = %q, want \"branch tip\"", rows[0].StateDetail)
	}
	if rows[0].Lock.State != crons.LockFollows {
		t.Errorf("lock state = %q, want %q", rows[0].Lock.State, crons.LockFollows)
	}
}

func TestArmPushed_KeepsPauseCursorAndOverrideAndWithdrawsTheRest(t *testing.T) {
	svc := pushedService(t)
	ctx := context.Background()

	push := crons.ArmPush{
		RepoURL: pushedRepoURL,
		Branch:  "main",
		SHA:     "0123456789abcdef0123456789abcdef01234567",
		Entries: []crons.Declared{
			declaredEntry("nightly", "default", "0 3 * * *"),
			declaredEntry("sweep", "quick", "*/5 * * * *"),
		},
	}
	if _, err := svc.ArmPushed(ctx, push); err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	nightly := crons.ScheduleID(pushedRepoURL, "nightly", "default")
	if err := svc.Pause(ctx, nightly); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	cron := "0 5 * * *"
	if _, err := svc.SetOverride(ctx, nightly, crons.Override{Cron: &cron}); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}

	push.Entries = push.Entries[:1]
	report, err := svc.ArmPushed(ctx, push)
	if err != nil {
		t.Fatalf("second ArmPushed: %v", err)
	}
	if report.Refreshed != 1 || report.Withdrawn != 1 {
		t.Fatalf("report = %+v, want one refreshed and one withdrawn", report)
	}
	if len(report.Withdrawals) != 1 || report.Withdrawals[0] != "acme/widgets/sweep/quick" {
		t.Errorf("withdrawals = %v", report.Withdrawals)
	}
	row, _, err := svc.Show(ctx, nightly, 1)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !row.Paused {
		t.Error("the push cleared the pause")
	}
	if row.Effective.Cron != cron {
		t.Errorf("effective cron = %q, want the override %q", row.Effective.Cron, cron)
	}
}

func TestArmPushed_RefusesARepositoryThatIsNotACloneURL(t *testing.T) {
	svc := pushedService(t)
	for _, repo := range []string{"", "/home/me/code/widgets"} {
		if _, err := svc.ArmPushed(context.Background(), crons.ArmPush{RepoURL: repo}); err == nil {
			t.Errorf("ArmPushed(%q) was accepted", repo)
		}
	}
}

func TestDisarmRepoURL_RemovesEveryRowOfOneRepository(t *testing.T) {
	svc := pushedService(t)
	ctx := context.Background()

	if _, err := svc.ArmPushed(ctx, crons.ArmPush{
		RepoURL: pushedRepoURL,
		Entries: []crons.Declared{
			declaredEntry("nightly", "default", "0 3 * * *"),
			declaredEntry("sweep", "quick", "*/5 * * * *"),
		},
	}); err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	removed, err := svc.DisarmRepoURL(ctx, pushedRepoURL)
	if err != nil {
		t.Fatalf("DisarmRepoURL: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %d, want none", len(rows))
	}
	if _, err := svc.DisarmRepoURL(ctx, ""); err == nil {
		t.Error("an empty repository URL was accepted")
	}
}

func TestResolve_AcceptsAPushedDisplayName(t *testing.T) {
	svc := pushedService(t)
	ctx := context.Background()

	if _, err := svc.ArmPushed(ctx, crons.ArmPush{
		RepoURL: pushedRepoURL,
		Entries: []crons.Declared{declaredEntry("sweep", "quick", "*/5 * * * *")},
	}); err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	want := crons.ScheduleID(pushedRepoURL, "sweep", "quick")
	for _, name := range []string{want, "acme/widgets/sweep/quick", "sweep/quick", "sweep"} {
		sched, err := svc.Resolve(ctx, name)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		if sched.ID != want {
			t.Errorf("Resolve(%q) = %s, want %s", name, sched.ID, want)
		}
	}
}

func TestRefresh_LeavesPushedRowsAlone(t *testing.T) {
	svc := pushedService(t)
	ctx := context.Background()

	if _, err := svc.ArmPushed(ctx, crons.ArmPush{
		RepoURL: pushedRepoURL,
		Branch:  "main",
		Entries: []crons.Declared{declaredEntry("nightly", "default", "0 3 * * *")},
	}); err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	// safety: no checkout of the pushed repository exists on this machine, so
	// a refresh that read one would fail the row or withdraw it.
	report, err := svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Errorf("errors = %v, want none", report.Errors)
	}
	if report.Repos != 0 || report.Withdrawn != 0 || report.Updated != 0 {
		t.Errorf("report = %+v, want nothing read or changed", report)
	}
	if report.Locked != 1 {
		t.Errorf("locked = %d, want the pushed row counted as left alone", report.Locked)
	}
	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !rows[0].Declared {
		t.Error("the refresh withdrew a pushed schedule")
	}
}

func TestControllerHealth_ReportsTheLoopAndTheStoredTick(t *testing.T) {
	svc := pushedService(t)
	ctx := context.Background()

	if _, err := svc.ArmPushed(ctx, crons.ArmPush{
		RepoURL: pushedRepoURL,
		Entries: []crons.Declared{declaredEntry("nightly", "default", "0 3 * * *")},
	}); err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	health, err := svc.ControllerHealth(ctx)
	if err != nil {
		t.Fatalf("ControllerHealth: %v", err)
	}
	if health.Timer.Detail != crons.ControllerTimerDetail {
		t.Errorf("timer detail = %q, want %q", health.Timer.Detail, crons.ControllerTimerDetail)
	}
	if !health.Timer.Enabled || !health.Timer.Installed {
		t.Errorf("timer = %+v, want the loop reported as running", health.Timer)
	}
	if health.Armed != 1 {
		t.Errorf("armed = %d, want 1", health.Armed)
	}
	if !health.TickStale {
		t.Error("a controller that has never ticked reports a fresh tick")
	}
	if health.Healthy() {
		t.Error("a controller with an armed schedule and no tick reports healthy")
	}

	at := time.Now().Add(-10 * time.Minute)
	if err := svc.Store.RecordCronTick(ctx, store.CronTick{At: at, Host: "controller-1"}); err != nil {
		t.Fatalf("RecordCronTick: %v", err)
	}
	health, err = svc.ControllerHealth(ctx)
	if err != nil {
		t.Fatalf("ControllerHealth: %v", err)
	}
	if !health.TickStale {
		t.Error("a tick ten minutes old is not reported stale")
	}
	if health.LastTick.Host != "controller-1" {
		t.Errorf("last tick host = %q", health.LastTick.Host)
	}
}

func TestPushedRepo_TellsACloneURLFromACheckout(t *testing.T) {
	for _, url := range []string{
		"https://github.com/acme/widgets.git",
		"http://git.internal/acme/widgets",
		"ssh://git@github.com/acme/widgets.git",
		"git@github.com:acme/widgets.git",
	} {
		if !crons.PushedRepo(url) {
			t.Errorf("PushedRepo(%q) = false", url)
		}
	}
	for _, path := range []string{"/home/me/code/widgets", "C:/code/widgets", ""} {
		if crons.PushedRepo(path) {
			t.Errorf("PushedRepo(%q) = true", path)
		}
	}
}
