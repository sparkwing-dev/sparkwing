package store_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func requireEventRefusal(t *testing.T, err error, limit string) {
	t.Helper()
	var refusal *store.StorageQuotaError
	if !errors.As(err, &refusal) || refusal.Limit != limit {
		t.Fatalf("append = %v, want a %s refusal", err, limit)
	}
}

// With no storage tier configured a runner token still cannot grow the
// events table without bound: the default per-event cap holds.
func TestEventCapsHoldWithNoStorageQuotaConfigured(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	limits, err := st.EventLimits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if limits != store.DefaultEventLimits {
		t.Fatalf("unconfigured limits = %+v, want the defaults %+v", limits, store.DefaultEventLimits)
	}
	big := bytes.Repeat([]byte("x"), int(store.DefaultEventLimits.MaxBytesPerEvent)+1)
	_, err = st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "custom", big)
	requireEventRefusal(t, err, store.StorageLimitEventBytes)
	if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "custom", []byte(`{}`)); err != nil {
		t.Fatalf("a small event under the defaults: %v", err)
	}
}

// The operator's caps replace the defaults, and the per-run cap counts what
// the run already stored.
func TestEventCapsFollowTheOperatorsLimits(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEventLimits(ctx, store.EventLimits{MaxBytesPerEvent: 1024, MaxBytesPerRun: 3 * 1024}); err != nil {
		t.Fatal(err)
	}
	kib := bytes.Repeat([]byte("x"), 1024-len("custom"))
	_, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "custom", append(kib, 'x'))
	requireEventRefusal(t, err, store.StorageLimitEventBytes)
	for i := range 3 {
		if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "custom", kib); err != nil {
			t.Fatalf("append %d inside the run cap: %v", i, err)
		}
	}
	_, err = st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "custom", kib)
	requireEventRefusal(t, err, store.StorageLimitEventBytesPerRun)

	if err := st.CreateRun(ctx, store.Run{ID: "run-2", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-2", "", "custom", kib); err != nil {
		t.Fatalf("another run's cap is its own: %v", err)
	}
}

// Concurrent appends on one run share the cap: exactly as many fit as the
// cap holds, however they interleave.
func TestConcurrentEventAppendsNeverPassTheRunCap(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEventLimits(ctx, store.EventLimits{MaxBytesPerEvent: 1024, MaxBytesPerRun: 3 * 1024}); err != nil {
		t.Fatal(err)
	}
	kib := bytes.Repeat([]byte("x"), 1024-len("custom"))
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "custom", kib)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	written := 0
	for err := range results {
		switch {
		case err == nil:
			written++
		case errors.Is(err, store.ErrStorageQuota):
		default:
			t.Errorf("concurrent append: %v", err)
		}
	}
	if written != 3 {
		t.Fatalf("%d appends of 1 KiB passed a 3 KiB run cap, want 3", written)
	}
}

// An event's kind is stored beside its payload, so the byte caps count both:
// a large kind cannot ride under a cap sized for payloads.
func TestEventCapsCountTheKind(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEventLimits(ctx, store.EventLimits{MaxBytesPerEvent: 1024, MaxBytesPerRun: 3 * 1024}); err != nil {
		t.Fatal(err)
	}
	kind := string(bytes.Repeat([]byte("k"), 100))
	_, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", kind, bytes.Repeat([]byte("x"), 1000))
	requireEventRefusal(t, err, store.StorageLimitEventBytes)
	fits := bytes.Repeat([]byte("x"), 1024-len(kind))
	for i := range 3 {
		if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", kind, fits); err != nil {
			t.Fatalf("append %d inside the run cap: %v", i, err)
		}
	}
	_, err = st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "k", nil)
	requireEventRefusal(t, err, store.StorageLimitEventBytesPerRun)
}

// A kind names a category: long or free-form kinds are refused before they
// reach the events table.
func TestEventKindIsShortAndPlain(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	longest := string(bytes.Repeat([]byte("k"), store.MaxEventKindBytes))
	for _, kind := range []string{"", longest + "k", "node started", "kind\n", "kind\x00"} {
		if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", kind, nil); !errors.Is(err, store.ErrInvalidEventKind) {
			t.Errorf("kind %q: append = %v, want ErrInvalidEventKind", kind, err)
		}
	}
	for _, kind := range []string{longest, "node_started", "sparkwing.step:log-1"} {
		if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", kind, nil); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}
}

// A run's event count is capped as well as its bytes, so many empty events
// cannot grow the table where the byte cap does not reach.
func TestRunEventCountIsCapped(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	for _, id := range []string{"run-1", "run-2"} {
		if err := st.CreateRun(ctx, store.Run{ID: id, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if store.DefaultEventLimits.MaxEventsPerRun <= 0 {
		t.Fatalf("default limits %+v leave the event count unbounded", store.DefaultEventLimits)
	}
	if err := st.SetEventLimits(ctx, store.EventLimits{MaxBytesPerEvent: 1024, MaxBytesPerRun: 1 << 20, MaxEventsPerRun: 3}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "k", nil); err != nil {
			t.Fatalf("append %d inside the count cap: %v", i, err)
		}
	}
	_, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "k", nil)
	requireEventRefusal(t, err, store.StorageLimitEventsPerRun)
	if _, err := st.AppendEventCharged(ctx, "agent:runner", "run-2", "", "k", nil); err != nil {
		t.Fatalf("another run's count is its own: %v", err)
	}
}

// Events appended without a charge, the controller's own, still count
// toward the run's caps.
func TestUnchargedEventsCountTowardTheRunCaps(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEventLimits(ctx, store.EventLimits{MaxBytesPerEvent: 1024, MaxBytesPerRun: 2048, MaxEventsPerRun: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, "run-1", "", "k", bytes.Repeat([]byte("x"), 2000)); err != nil {
		t.Fatal(err)
	}
	_, err := st.AppendEventCharged(ctx, "agent:runner", "run-1", "", "k", bytes.Repeat([]byte("x"), 100))
	requireEventRefusal(t, err, store.StorageLimitEventBytesPerRun)
}
