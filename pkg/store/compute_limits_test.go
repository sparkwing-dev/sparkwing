package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func setLimit(t *testing.T, s *store.Store, name string, value int64) {
	t.Helper()
	if err := s.SetComputeLimit(context.Background(), name, value); err != nil {
		t.Fatalf("set %s: %v", name, err)
	}
}

func TestComputeLimitsAreUnlimitedUntilSet(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.Any() {
		t.Fatalf("a fresh store reads %+v, want every guard at zero", limits)
	}
	for _, name := range store.ComputeLimitNames() {
		if v, ok := limits.Value(name); !ok || v != 0 {
			t.Fatalf("guard %s = %d (known %v), want 0", name, v, ok)
		}
	}
}

func TestSetComputeLimitRejectsUnknownAndNegative(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if err := s.SetComputeLimit(ctx, "max_dollars", 5); err == nil {
		t.Fatal("an unknown guard was accepted")
	}
	if err := s.SetComputeLimit(ctx, store.ComputeLimitNodesPerRun, -1); err == nil {
		t.Fatal("a negative ceiling was accepted")
	}
	setLimit(t, s, store.ComputeLimitNodesPerRun, 7)
	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.NodesPerRun != 7 {
		t.Fatalf("nodes per run = %d, want 7", limits.NodesPerRun)
	}
	setLimit(t, s, store.ComputeLimitNodesPerRun, 0)
	limits, err = s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.Any() {
		t.Fatalf("limits = %+v, want the guard removed", limits)
	}
}

func TestSetComputeLimitsWritesEveryNamedGuardOrNone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	limits, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 7,
		store.ComputeLimitRunSeconds:    10,
	})
	if err != nil {
		t.Fatalf("set limits: %v", err)
	}
	if limits.GlobalRunners != 7 || limits.RunSeconds != 10 {
		t.Fatalf("limits = %+v", limits)
	}
	if _, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 8,
		store.ComputeLimitRunSeconds:    -1,
	}); !errors.Is(err, store.ErrInvalidComputeLimitSetting) {
		t.Fatalf("mixed update = %v, want invalid setting", err)
	}
	limits, err = s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("read limits: %v", err)
	}
	if limits.GlobalRunners != 7 {
		t.Fatalf("the good half of a refused update set the global cap to %d", limits.GlobalRunners)
	}
}

func TestSetComputeLimitsRollsBackWhenOneGuardWriteFails(t *testing.T) {
	s := storetest.OpenSQLite(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(`CREATE TRIGGER refuse_scale_step BEFORE INSERT ON settings
WHEN NEW.key = 'compute_limit_max_run_seconds'
BEGIN SELECT RAISE(ABORT, 'run seconds refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 7,
		store.ComputeLimitRunSeconds:    10,
	}); err == nil {
		t.Fatal("the rejected compute settings write succeeded")
	}
	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("read limits: %v", err)
	}
	if limits.Any() {
		t.Fatalf("the failed transaction changed limits: %+v", limits)
	}
}

func TestAlarmStateCountsNothingWithoutAnAlarm(t *testing.T) {
	s := storetest.Open(t)
	runners, alarm, err := s.ComputeAlarmState(context.Background())
	if err != nil {
		t.Fatalf("alarm state: %v", err)
	}
	if runners != 0 || alarm != 0 {
		t.Fatalf("alarm state = (%d, %d), want zeros when no alarm is set", runners, alarm)
	}
}
