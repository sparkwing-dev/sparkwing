package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMetrics_RoundTrip(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-1",
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "a", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	for i, millicores := range []int64{100, 200, 350} {
		sample := store.MetricSample{
			TS:            now.Add(time.Duration(i) * time.Second),
			CPUMillicores: millicores,
			MemoryBytes:   int64(1024 * 1024 * (i + 1)),
		}
		if err := st.AddNodeMetricSample(ctx, "run-1", "a", sample); err != nil {
			t.Fatalf("AddNodeMetricSample: %v", err)
		}
	}

	samples, err := st.ListNodeMetrics(ctx, "run-1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatalf("samples=%d want 3", len(samples))
	}
	if samples[0].CPUMillicores != 100 || samples[2].CPUMillicores != 350 {
		t.Errorf("sample ordering: %+v", samples)
	}

	dup := samples[0]
	if err := st.AddNodeMetricSample(ctx, "run-1", "a", dup); err != nil {
		t.Fatalf("duplicate insert returned error: %v", err)
	}
	after, _ := st.ListNodeMetrics(ctx, "run-1", "a")
	if len(after) != 3 {
		t.Errorf("duplicate insert changed count: %d", len(after))
	}
}

func TestNodeMetricsCapAndPages(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "a", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0)
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	insert := storetest.Rebind(st, `INSERT INTO node_metrics (team, run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
SELECT team, id, 'a', ?, ?, 1, 0 FROM runs WHERE id = 'run-1'`)
	for i := range store.MaxNodeMetricSamples - 1 {
		if _, err := tx.ExecContext(ctx, insert, base.Add(time.Duration(i)*time.Second).UnixNano(), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	next := func(i int) store.MetricSample {
		return store.MetricSample{TS: base.Add(time.Duration(i) * time.Second), CPUMillicores: int64(i), MemoryBytes: 1}
	}
	if err := st.AddNodeMetricSample(ctx, "run-1", "a", next(store.MaxNodeMetricSamples-1)); err != nil {
		t.Fatalf("the last sample under the cap: %v", err)
	}
	if err := st.AddNodeMetricSample(ctx, "run-1", "a", next(store.MaxNodeMetricSamples)); !errors.Is(err, store.ErrNodeMetricLimit) {
		t.Fatalf("a sample past the cap = %v, want ErrNodeMetricLimit", err)
	}

	first, err := st.ListNodeMetricsPage(ctx, "run-1", "a", time.Time{}, 3)
	if err != nil || len(first) != 3 || first[0].CPUMillicores != 0 || first[2].CPUMillicores != 2 {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := st.ListNodeMetricsPage(ctx, "run-1", "a", first[2].TS, 3)
	if err != nil || len(second) != 3 || second[0].CPUMillicores != 3 {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	all, err := st.ListNodeMetrics(ctx, "run-1", "a")
	if err != nil || len(all) != store.MaxNodeMetricSamples {
		t.Fatalf("every sample = %d, %v", len(all), err)
	}
}
