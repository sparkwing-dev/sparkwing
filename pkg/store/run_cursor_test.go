package store_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestListRuns_CursorWalksRunsSharingAnInstantExactlyOnce(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	const runs = 12
	instant := time.Unix(1700000000, 0)
	for i := range runs {
		if err := st.CreateRun(ctx, store.Run{
			ID:        "run-tie-" + strconv.Itoa(i),
			Pipeline:  "checks",
			Status:    "success",
			StartedAt: instant,
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]int{}
	var filter store.RunFilter
	filter.Limit = 5
	for page := 0; page < runs+2; page++ {
		got, err := st.ListRuns(ctx, filter)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(got) == 0 {
			break
		}
		for _, r := range got {
			seen[r.ID]++
		}
		last := got[len(got)-1]
		filter.AfterStartedAt, filter.AfterID = last.StartedAt.UnixNano(), last.ID
	}

	if len(seen) != runs {
		t.Errorf("walked %d distinct runs, want %d", len(seen), runs)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("run %s served %d times, want once", id, n)
		}
	}
}

func TestCountRuns_IgnoresLimitAndHonoursTheCursor(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	const runs = 30
	for i := range runs {
		if err := st.CreateRun(ctx, store.Run{
			ID:        "run-count-" + strconv.Itoa(i),
			Pipeline:  "checks",
			Status:    "success",
			StartedAt: time.Unix(int64(1700000000+i), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.CountRuns(ctx, store.RunFilter{Limit: 5})
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	if got != runs {
		t.Errorf("CountRuns with Limit 5 = %d, want %d, the limit bounds a page, not the count", got, runs)
	}

	newest := time.Unix(1700000000+runs-1, 0)
	remaining, err := st.CountRuns(ctx, store.RunFilter{
		AfterStartedAt: newest.UnixNano(),
		AfterID:        "run-count-" + strconv.Itoa(runs-1),
	})
	if err != nil {
		t.Fatalf("CountRuns after a cursor: %v", err)
	}
	if remaining != runs-1 {
		t.Errorf("CountRuns after the newest run = %d, want %d", remaining, runs-1)
	}
}

func TestListRuns_CursorResumesAfterARunAtTheEpoch(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	for i := range 4 {
		if err := st.CreateRun(ctx, store.Run{
			ID:        "run-epoch-" + strconv.Itoa(i),
			Pipeline:  "checks",
			Status:    "success",
			StartedAt: time.Unix(0, 0),
		}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := st.ListRuns(ctx, store.RunFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	last := first[len(first)-1]
	next, err := st.ListRuns(ctx, store.RunFilter{
		Limit: 2, AfterStartedAt: last.StartedAt.UnixNano(), AfterID: last.ID,
	})
	if err != nil {
		t.Fatalf("ListRuns after a cursor: %v", err)
	}
	for _, r := range next {
		if r.ID == last.ID {
			t.Fatalf("the cursor re-served %s, so a walk over epoch runs cannot terminate", r.ID)
		}
	}
	if len(next) != 2 {
		t.Errorf("second page held %d runs, want 2", len(next))
	}
}

func TestListRuns_CursorWalksPastNeverStartedRuns(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	const started, queued = 3, 5
	for i := range started {
		if err := st.CreateRun(ctx, store.Run{
			ID: "run-live-" + strconv.Itoa(i), Pipeline: "checks", Status: "success",
			StartedAt: time.Unix(int64(1700000000+i), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range queued {
		if err := st.CreateRun(ctx, store.Run{
			ID: "run-queued-" + strconv.Itoa(i), Pipeline: "checks", Status: "pending",
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]int{}
	var filter store.RunFilter
	filter.Limit = 2
	for range started + queued + 2 {
		page, err := st.ListRuns(ctx, filter)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			seen[r.ID]++
		}
		last := page[len(page)-1]
		instant := last.StartedAt.UnixNano()
		if last.StartedAt.IsZero() {
			instant = 0
		}
		filter.AfterStartedAt, filter.AfterID = instant, last.ID
	}

	if len(seen) != started+queued {
		t.Errorf("walked %d distinct runs, want %d, the never-started tail was not reached", len(seen), started+queued)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("run %s served %d times", id, n)
		}
	}
}
