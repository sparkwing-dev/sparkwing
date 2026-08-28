package sparkwing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func testBudget(t *testing.T) *sparkwing.ConcurrencyGroup {
	t.Helper()
	return sparkwing.BoxToolBudget("golangci-lint", 8, time.Minute)
}

func TestToolSlot_NoProviderFallsBackRatherThanProceeding(t *testing.T) {
	release, granted := sparkwing.ToolSlot(context.Background(), testBudget(t), 100)
	if granted {
		t.Fatal("reported a granted budget with no provider installed, so a caller would drop its tool lock while nothing bounded it")
	}
	if release == nil {
		t.Fatal("release is nil, so `defer release()` would panic on the fallback path")
	}
	release()
}

func TestToolSlot_AcquireFailureFallsBack(t *testing.T) {
	ctx := sparkwing.WithToolSlotProvider(context.Background(),
		func(context.Context, *sparkwing.ConcurrencyGroup, int) (func(), error) {
			return nil, errors.New("daemon went away")
		})

	release, granted := sparkwing.ToolSlot(ctx, testBudget(t), 100)
	if granted {
		t.Fatal("a failed acquire reported success, which would leave the tool unserialized")
	}
	release()
}

func TestToolSlot_GrantedReturnsProviderRelease(t *testing.T) {
	released := 0
	ctx := sparkwing.WithToolSlotProvider(context.Background(),
		func(context.Context, *sparkwing.ConcurrencyGroup, int) (func(), error) {
			return func() { released++ }, nil
		})

	release, granted := sparkwing.ToolSlot(ctx, testBudget(t), 100)
	if !granted {
		t.Fatal("a successful acquire did not report the budget as held")
	}
	release()
	if released != 1 {
		t.Fatalf("provider release called %d times, want 1", released)
	}
}

func TestToolSlot_GrantedWithNilReleaseIsStillSafeToDefer(t *testing.T) {
	ctx := sparkwing.WithToolSlotProvider(context.Background(),
		func(context.Context, *sparkwing.ConcurrencyGroup, int) (func(), error) {
			return nil, nil
		})

	release, granted := sparkwing.ToolSlot(ctx, testBudget(t), 100)
	if !granted {
		t.Fatal("a nil-error acquire was not reported as granted")
	}
	release()
}

func TestToolSlot_NilGroupIsNotGranted(t *testing.T) {
	release, granted := sparkwing.ToolSlot(context.Background(), nil)
	if granted {
		t.Fatal("a nil group reported a held budget")
	}
	release()
}

func TestToolSlot_PassesTheDeclaredCostThrough(t *testing.T) {
	var gotCost int
	ctx := sparkwing.WithToolSlotProvider(context.Background(),
		func(_ context.Context, _ *sparkwing.ConcurrencyGroup, cost int) (func(), error) {
			gotCost = cost
			return func() {}, nil
		})

	release, _ := sparkwing.ToolSlot(ctx, testBudget(t), 400)
	defer release()

	if gotCost != 400 {
		t.Fatalf("provider charged %d units, want the declared 400", gotCost)
	}
}

func TestBoxToolBudget_CapacityIsCentiCores(t *testing.T) {
	g := sparkwing.BoxToolBudget("t", 8, time.Minute)
	if got := g.Limit().Capacity; got != 800 {
		t.Fatalf("capacity %d, want 800 centicores for 8 grantable cores", got)
	}
	if got := g.Limit().Scope; got != sparkwing.ScopeBox {
		t.Fatalf("scope %q, want box", got)
	}
	if got := g.Limit().OnLimit; got != sparkwing.Queue {
		t.Fatalf("on-limit %q, want queue so a contended step waits", got)
	}
}

func TestBoxToolBudget_TinyBoxStillGetsAWholeUnit(t *testing.T) {
	if got := sparkwing.BoxToolBudget("t", 0, time.Minute).Limit().Capacity; got < 1 {
		t.Fatalf("capacity %d on a zero-core budget, which admits nothing forever", got)
	}
}

func TestToolCostCenticores(t *testing.T) {
	for _, tc := range []struct {
		cores float64
		want  int
	}{
		{cores: 4.0, want: 400},
		{cores: 0.85, want: 85},
		{cores: 0, want: 1},
		{cores: 0.001, want: 1},
	} {
		if got := sparkwing.ToolCostCenticores(tc.cores); got != tc.want {
			t.Fatalf("ToolCostCenticores(%v) = %d, want %d", tc.cores, got, tc.want)
		}
	}
}
