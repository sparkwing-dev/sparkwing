package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestRunCronTick_RunsOnceAndBlocksTheSameMinute(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	ran := 0
	first, err := st.AsOperator().RunCronTick(ctx, func(context.Context) error {
		ran++
		return nil
	})
	if err != nil || !first {
		t.Fatalf("first tick: ran=%v err=%v, want a tick", first, err)
	}

	second, err := st.AsOperator().RunCronTick(ctx, func(context.Context) error {
		ran++
		return nil
	})
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if second {
		t.Error("a second caller ticked inside the same minute")
	}
	if ran != 1 {
		t.Errorf("tick function ran %d times, want 1", ran)
	}
}

func TestRunCronTick_RetriesAfterAFailedTick(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	boom := errors.New("the store went away")
	ran, err := st.AsOperator().RunCronTick(ctx, func(context.Context) error {
		return boom
	})
	if !ran {
		t.Fatal("the tick did not run")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the tick's own failure", err)
	}
	// safety: a failed tick stamps nothing, so the next minute is free rather
	// than suppressed for the whole interval by a tick that did no work.
	again, err := st.AsOperator().RunCronTick(ctx, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if !again {
		t.Error("a failed tick suppressed its window")
	}
}

func TestRunCronTick_RefusesAMissingTickFunction(t *testing.T) {
	st := storetest.Open(t)
	if _, err := st.AsOperator().RunCronTick(context.Background(), nil); err == nil {
		t.Fatal("a nil tick function was accepted")
	}
}

func TestRunCronTick_RetriesAfterACancelledTick(t *testing.T) {
	st := storetest.Open(t)
	ctx, cancel := context.WithCancel(context.Background())

	ran, err := st.AsOperator().RunCronTick(ctx, func(tickCtx context.Context) error {
		cancel()
		return tickCtx.Err()
	})
	if !ran {
		t.Fatalf("the cancelled tick did not run: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("tick error = %v, want the cancellation", err)
	}
	assertCronTickRetries(t, st)
}

func TestRunCronTick_RecoversAPanicAndRetries(t *testing.T) {
	st := storetest.Open(t)

	ran, err := st.AsOperator().RunCronTick(context.Background(), func(context.Context) error {
		panic("the evaluator fell over")
	})
	if !ran {
		t.Error("a panicking tick reported that it never ran")
	}
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("tick error = %v, want the recovered panic", err)
	}
	assertCronTickRetries(t, st)
}

// safety: a tick that failed leaves no stamp, so the next caller ticks.
func assertCronTickRetries(t *testing.T, st *store.Store) {
	t.Helper()
	next, err := st.AsOperator().RunCronTick(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("the next tick: %v", err)
	}
	if !next {
		t.Error("a failed tick suppressed the next minute")
	}
}
