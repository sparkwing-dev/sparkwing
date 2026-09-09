package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestRunCronTickLeased_RunsOnceAndBlocksTheSameMinute(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	ran := 0
	first, err := st.RunCronTickLeased(ctx, "alpha", time.Minute, func(context.Context) error {
		ran++
		return nil
	})
	if err != nil || !first {
		t.Fatalf("first tick: ran=%v err=%v, want a tick", first, err)
	}

	second, err := st.RunCronTickLeased(ctx, "beta", time.Minute, func(context.Context) error {
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

func TestRunCronTickLeased_ConcurrentClaimantLoses(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	// safety: the first claimant holds the lease inside fn, so the second
	// meets a live claim rather than a stamp, which is the race a second
	// controller in the same store runs into.
	entered := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	var firstRan, secondRan bool
	var firstErr, secondErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		firstRan, firstErr = st.RunCronTickLeased(ctx, "alpha", time.Minute, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()

	<-entered
	secondRan, secondErr = st.RunCronTickLeased(ctx, "beta", time.Minute, func(context.Context) error {
		t.Error("the losing claimant ran the tick")
		return nil
	})
	close(release)
	wg.Wait()

	if firstErr != nil || !firstRan {
		t.Fatalf("holder: ran=%v err=%v, want a tick", firstRan, firstErr)
	}
	if secondErr != nil {
		t.Fatalf("loser: %v", secondErr)
	}
	if secondRan {
		t.Error("two callers ticked one minute")
	}
}

func TestRunCronTickLeased_ReleasesTheClaimAfterAFailedTick(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	boom := errors.New("the store went away")
	ran, err := st.RunCronTickLeased(ctx, "alpha", time.Minute, func(context.Context) error {
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
	again, err := st.RunCronTickLeased(ctx, "beta", time.Minute, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if !again {
		t.Error("a failed tick left its window claimed")
	}
}

func TestRunCronTickLeased_RefusesAMissingTickFunction(t *testing.T) {
	st := storetest.Open(t)
	if _, err := st.RunCronTickLeased(context.Background(), "alpha", time.Minute, nil); err == nil {
		t.Fatal("a nil tick function was accepted")
	}
}

func TestCronTickWindowLeavesRoomForAMinuteTimer(t *testing.T) {
	// safety: a caller waking every minute stamps its tick a little after it
	// started, so a lease window of a full minute would turn the next wake away
	// and skip that minute entirely.
	if store.CronTickWindow >= time.Minute {
		t.Errorf("CronTickWindow = %s, want a window under a minute", store.CronTickWindow)
	}
	if store.CronTickWindow < 30*time.Second {
		t.Errorf("CronTickWindow = %s, want a window near a minute", store.CronTickWindow)
	}
}

func TestRunCronTickLeased_ReleasesTheClaimWhenTheTickIsCancelled(t *testing.T) {
	st := storetest.Open(t)
	ctx, cancel := context.WithCancel(context.Background())

	ran, err := st.RunCronTickLeased(ctx, "alpha", time.Hour, func(tickCtx context.Context) error {
		cancel()
		return tickCtx.Err()
	})
	if !ran {
		t.Fatalf("the cancelled tick did not run: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("tick error = %v, want the cancellation", err)
	}
	assertCronTickClaimFree(t, st)
}

func TestRunCronTickLeased_RecoversAPanicAndReleasesTheClaim(t *testing.T) {
	st := storetest.Open(t)

	ran, err := st.RunCronTickLeased(context.Background(), "alpha", time.Hour, func(context.Context) error {
		panic("the evaluator fell over")
	})
	if !ran {
		t.Error("a panicking tick reported that it never ran")
	}
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("tick error = %v, want the recovered panic", err)
	}
	assertCronTickClaimFree(t, st)
}

// safety: a tick that failed leaves no stamp, so the next caller is turned away
// only by a claim nobody released.
func assertCronTickClaimFree(t *testing.T, st *store.Store) {
	t.Helper()
	next, err := st.RunCronTickLeased(context.Background(), "beta", time.Minute,
		func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("the next tick: %v", err)
	}
	if !next {
		t.Error("the claim was still held, so the next minute could not tick")
	}
}
