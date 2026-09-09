package controller

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: records what each tick asked for, so a test can tell one dispatch
// from two.
type countingDispatcher struct {
	mu   sync.Mutex
	runs []RunRequest
}

func (d *countingDispatcher) Dispatch(_ context.Context, req RunRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs = append(d.runs, req)
	return nil
}

func (d *countingDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.runs)
}

func armPushedForTick(t *testing.T, st *store.Store) store.CronSchedule {
	t.Helper()
	svc := &crons.Service{Store: st}
	report, err := svc.ArmPushed(context.Background(), crons.ArmPush{
		RepoURL: "https://github.com/acme/widgets.git",
		Branch:  "main",
		SHA:     "0123456789abcdef0123456789abcdef01234567",
		Entries: []crons.Declared{{
			Pipeline: "nightly",
			Name:     store.CronScheduleDefaultName,
			Trigger: pipelines.ScheduleTrigger{
				Name: store.CronScheduleDefaultName, Cron: "* * * * *",
				Where: pipelines.ScheduleWhereController,
			},
		}},
	})
	if err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	if len(report.Schedules) != 1 {
		t.Fatalf("armed %d schedules, want one", len(report.Schedules))
	}
	return report.Schedules[0]
}

func TestCronTick_TwoControllersSharingAStoreFireOneTrigger(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sched := armPushedForTick(t, st)

	// safety: both evaluators read the same instant two minutes on, so one due
	// instant exists without the test waiting a minute for the clock.
	ahead := func() time.Time { return time.Now().Add(2 * time.Minute) }
	alpha := New(st, nil).WithDispatcher(&countingDispatcher{})
	alpha.cronHolder, alpha.cronNow = "alpha", ahead
	beta := New(st, nil).WithDispatcher(&countingDispatcher{})
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

	dispatched := alpha.dispatcher.(*countingDispatcher).count() + beta.dispatcher.(*countingDispatcher).count()
	if dispatched != 1 {
		t.Fatalf("two controllers dispatched %d runs for one due instant, want 1", dispatched)
	}

	triggers, err := st.ListTriggers(ctx, store.TriggerFilter{Pipelines: []string{"nightly"}})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want one", len(triggers))
	}
	trigger := triggers[0]
	if trigger.TriggerSource != cronTriggerSource {
		t.Errorf("trigger source = %q, want %q", trigger.TriggerSource, cronTriggerSource)
	}
	if trigger.TriggerEnv[crons.ScheduleEnvKey] != sched.ID {
		t.Errorf("trigger env = %v, want the schedule id", trigger.TriggerEnv)
	}
	if trigger.IdempotencyKey == "" {
		t.Error("the launch carried no idempotency key")
	}
	run, err := st.GetRun(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "pending" {
		t.Errorf("run status = %q, want pending", run.Status)
	}

	tick, err := st.GetCronTick(ctx)
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
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sched := armPushedForTick(t, st)

	srv := New(st, nil).WithDispatcher(&countingDispatcher{})
	launcher := cronLauncher{server: srv}
	due := time.Now().UTC().Truncate(time.Minute)

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
	if got := srv.dispatcher.(*countingDispatcher).count(); got != 1 {
		t.Errorf("dispatched %d runs, want 1", got)
	}
}

func TestCronLauncherActive_TreatsAnUnclaimedRunAsStale(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	now := time.Now()
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "run_old", Pipeline: "nightly", CreatedAt: now}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run_old", Pipeline: "nightly", Status: "pending",
		CreatedAt: now.Add(-2 * time.Hour), StartedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	launcher := cronLauncher{server: New(st, nil)}

	fresh, err := launcher.Active(ctx, "run_old", 4*time.Hour)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !fresh {
		t.Error("a run queued inside the window is not active")
	}
	stale, err := launcher.Active(ctx, "run_old", time.Minute)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if stale {
		t.Error("a run nothing claimed for two hours still reads as active")
	}
	missing, err := launcher.Active(ctx, "run_gone", time.Minute)
	if err != nil {
		t.Fatalf("Active on a missing run: %v", err)
	}
	if missing {
		t.Error("a run the store does not hold reads as active")
	}
}
