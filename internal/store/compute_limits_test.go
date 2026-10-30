package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func setLimit(t *testing.T, s *store.Store, name string, value int64) {
	t.Helper()
	if _, err := s.AsOperator().SetComputeLimits(context.Background(), map[string]int64{name: value}); err != nil {
		t.Fatalf("set %s: %v", name, err)
	}
}

func TestComputeLimitsAreUnlimitedUntilSet(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	limits, err := s.AsOperator().ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits != (store.ComputeLimits{}) {
		t.Fatalf("a fresh store reads %+v, want every guard at zero", limits)
	}
	for _, name := range store.ComputeLimitNames() {
		if v, ok := limits.Value(name); !ok || v != 0 {
			t.Fatalf("guard %s = %d (known %v), want 0", name, v, ok)
		}
	}
}

func TestSetComputeLimitsRejectsUnknownAndNegative(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.AsOperator().SetComputeLimits(ctx, map[string]int64{"max_dollars": 5}); err == nil {
		t.Fatal("an unknown guard was accepted")
	}
	if _, err := s.AsOperator().SetComputeLimits(ctx, map[string]int64{store.ComputeLimitNodesPerRun: -1}); err == nil {
		t.Fatal("a negative ceiling was accepted")
	}
	setLimit(t, s, store.ComputeLimitNodesPerRun, 7)
	limits, err := s.AsOperator().ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.NodesPerRun != 7 {
		t.Fatalf("nodes per run = %d, want 7", limits.NodesPerRun)
	}
	setLimit(t, s, store.ComputeLimitNodesPerRun, 0)
	limits, err = s.AsOperator().ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits != (store.ComputeLimits{}) {
		t.Fatalf("limits = %+v, want the guard removed", limits)
	}
}

func TestSetComputeLimitsWritesEveryNamedGuardOrNone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	limits, err := s.AsOperator().SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 7,
		store.ComputeLimitRunSeconds:    10,
	})
	if err != nil {
		t.Fatalf("set limits: %v", err)
	}
	if limits.GlobalRunners != 7 || limits.RunSeconds != 10 {
		t.Fatalf("limits = %+v", limits)
	}
	if _, err := s.AsOperator().SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 8,
		store.ComputeLimitRunSeconds:    -1,
	}); !errors.Is(err, store.ErrInvalidComputeLimitSetting) {
		t.Fatalf("mixed update = %v, want invalid setting", err)
	}
	limits, err = s.AsOperator().ComputeLimits(ctx)
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
	if _, err := s.AsOperator().SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 7,
		store.ComputeLimitRunSeconds:    10,
	}); err == nil {
		t.Fatal("the rejected compute settings write succeeded")
	}
	limits, err := s.AsOperator().ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("read limits: %v", err)
	}
	if limits != (store.ComputeLimits{}) {
		t.Fatalf("the failed transaction changed limits: %+v", limits)
	}
}

func TestComputeLimitErrorNamesUsageCapAndOperatorRemedy(t *testing.T) {
	err := (&store.ComputeLimitError{Limit: store.ComputeLimitRunsPerHour, Cap: 20, Observed: 20, Scope: "principal account"}).Error()
	for _, want := range []string{"max_runs_per_hour", "is at 20, cap 20", "ask the operator", "support@sparkwing.dev"} {
		if !strings.Contains(err, want) {
			t.Fatalf("compute refusal %q omits %q", err, want)
		}
	}
}
