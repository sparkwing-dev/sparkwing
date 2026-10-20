package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func checkStoreScheduleBoundary(t *testing.T) (err error) {
	ctx := t.Context()
	root := t.TempDir()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() {
		if s != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	if err := s.AsOperator().TestOnlyCreateTeam(ctx, "other"); err != nil {
		return err
	}
	now := time.Unix(1700000000, 83).UTC()
	repo := filepath.Join(root, "repo")
	for i, team := range []store.Team{store.DefaultTeam, "other"} {
		tenant, err := s.ForTeam(ctx, team)
		if err != nil {
			return err
		}
		row := store.CronSchedule{ID: fmt.Sprintf("schedule-%d", i), RepoPath: repo, Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC"}
		stored, created, err := tenant.ArmCronSchedule(ctx, row, now)
		if err != nil || !created || stored.Team != team || !stored.ArmedAt.Equal(now) {
			return fmt.Errorf("tenant schedule: %+v, %v, %v", stored, created, err)
		}
		fire := &store.CronFire{Outcome: store.CronOutcomeMissed, DueAt: now, DecidedAt: now, Detail: "past catch-up"}
		if err := tenant.ResolveCronDue(ctx, row.ID, now, nil, fire, now); err != nil {
			return err
		}
	}
	local, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	if _, err := local.GetCronSchedule(ctx, "schedule-1"); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("foreign schedule read: %v", err)
	}
	if err := local.SetCronSchedulePaused(ctx, "schedule-1", true, now); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("foreign schedule write: %v", err)
	}
	if fires, err := local.ListCronFires(ctx, "schedule-1", 10); err != nil || len(fires) != 0 {
		return fmt.Errorf("foreign schedule history: %+v, %v", fires, err)
	}
	before, err := local.GetCronSchedule(ctx, "schedule-0")
	if err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, `CREATE TRIGGER refuse_schedule_history BEFORE INSERT ON cron_fires BEGIN SELECT RAISE(ABORT,'history unavailable'); END`); err != nil {
		return err
	}
	later := now.Add(time.Minute)
	if err := local.ResolveCronDue(ctx, before.ID, later, &later, &store.CronFire{Outcome: store.CronOutcomeMissed, DueAt: later, DecidedAt: later}, later); err == nil {
		return errors.New("failed history write advanced the schedule")
	}
	if _, err := s.DB().ExecContext(ctx, `DROP TRIGGER refuse_schedule_history`); err != nil {
		return err
	}
	tick := store.CronTick{At: later, Host: "controller", Version: "fixture", Error: "history unavailable"}
	if err := s.AsOperator().RecordCronTick(ctx, tick); err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	s, err = target.TryOpen()
	if err != nil {
		return err
	}
	local, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	after, err := local.GetCronSchedule(ctx, before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("schedule rollback: %+v -> %+v, %v", before, after, err)
	}
	fires, err := local.ListCronFires(ctx, before.ID, 10)
	if err != nil || len(fires) != 1 || fires[0].RunID != "" || !fires[0].DecidedAt.Equal(now) {
		return fmt.Errorf("persisted schedule history: %+v, %v", fires, err)
	}
	if got, err := s.AsOperator().GetCronTick(ctx); err != nil || !got.At.Equal(tick.At) || got.Host != tick.Host || got.Version != tick.Version || got.Error != tick.Error {
		return fmt.Errorf("persisted tick: %+v, %v", got, err)
	}
	if n, err := local.DeleteCronSchedulesForRepo(ctx, repo); err != nil || n != 1 {
		return fmt.Errorf("tenant schedule deletion: %d, %v", n, err)
	}
	other, err := s.ForTeam(ctx, "other")
	if err != nil {
		return err
	}
	if row, err := other.GetCronSchedule(ctx, "schedule-1"); err != nil || row.Paused || row.LastOutcome != store.CronOutcomeMissed {
		return fmt.Errorf("foreign schedule after deletion: %+v, %v", row, err)
	}
	return nil
}

func TestStoreScheduleBoundary(t *testing.T) {
	if err := checkStoreScheduleBoundary(t); err != nil {
		t.Fatal(err)
	}
}
