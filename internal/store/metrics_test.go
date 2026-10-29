package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestMetrics_RoundTrip(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
	a := metricAuthorization(t, f, "metrics")
	ctx := t.Context()

	now := time.Now().UTC()
	for i, millicores := range []int64{100, 200, 350} {
		sample := store.MetricSample{
			TS:            now.Add(time.Duration(i) * time.Second),
			CPUMillicores: millicores,
			MemoryBytes:   int64(1024 * 1024 * (i + 1)),
		}
		if err := f.tenant.WriteMetric(ctx, a, sample); err != nil {
			t.Fatalf("AddNodeMetricSample: %v", err)
		}
	}

	samples, err := f.tenant.ListNodeMetrics(ctx, string(f.ref.RunID), string(f.ref.NodeID))
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
	if err := f.tenant.WriteMetric(ctx, a, dup); err != nil {
		t.Fatalf("duplicate insert returned error: %v", err)
	}
	after, _ := f.tenant.ListNodeMetrics(ctx, string(f.ref.RunID), string(f.ref.NodeID))
	if len(after) != 3 {
		t.Errorf("duplicate insert changed count: %d", len(after))
	}
}

func TestNodeMetricsCapAndPages(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.1s measured in the short class; the full suite runs it")
	}
	f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
	a := metricAuthorization(t, f, "metrics")
	ctx := t.Context()
	base := time.Unix(1_700_000_000, 0)
	tx, err := f.s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	insert := storetest.Rebind(f.s, `INSERT INTO node_metrics (team, run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
SELECT team, id, ?, ?, ?, 1, 0 FROM runs WHERE team = ? AND id = ?`)
	for i := range store.MaxNodeMetricSamples - 1 {
		if _, err := tx.ExecContext(ctx, insert, f.ref.NodeID, base.Add(time.Duration(i)*time.Second).UnixNano(), i, f.tenant.Team(), f.ref.RunID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	next := func(i int) store.MetricSample {
		return store.MetricSample{TS: base.Add(time.Duration(i) * time.Second), CPUMillicores: int64(i), MemoryBytes: 1}
	}
	if err := f.tenant.WriteMetric(ctx, a, next(store.MaxNodeMetricSamples-1)); err != nil {
		t.Fatalf("the last sample under the cap: %v", err)
	}
	if err := f.tenant.WriteMetric(ctx, a, next(store.MaxNodeMetricSamples-1)); err != nil {
		t.Fatalf("identical retry at the cap: %v", err)
	}

	if err := f.tenant.WriteMetric(ctx, a, next(store.MaxNodeMetricSamples)); !errors.Is(err, store.ErrNodeMetricLimit) {
		t.Fatalf("a sample past the cap = %v, want ErrNodeMetricLimit", err)
	}
	for i := range 8 {
		marker := store.MetricSample{Kind: store.MetricPartial, TS: base.Add(time.Duration(store.MaxNodeMetricSamples+1+i) * time.Second)}
		if err := f.tenant.WriteMetric(ctx, a, marker); err != nil {
			t.Fatalf("marker %d past the cap: %v", i, err)
		}
	}
	overflow := store.MetricSample{Kind: store.MetricUnknown, TS: base.Add(time.Duration(store.MaxNodeMetricSamples+1+8) * time.Second)}
	if err := f.tenant.WriteMetric(ctx, a, overflow); !errors.Is(err, store.ErrNodeMetricLimit) {
		t.Fatalf("a marker past the marker headroom = %v, want ErrNodeMetricLimit", err)
	}

	first, err := f.tenant.ListNodeMetricsPage(ctx, string(f.ref.RunID), string(f.ref.NodeID), time.Time{}, 3)
	if err != nil || len(first) != 3 || first[0].CPUMillicores != 0 || first[2].CPUMillicores != 2 {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := f.tenant.ListNodeMetricsPage(ctx, string(f.ref.RunID), string(f.ref.NodeID), first[2].TS, 3)
	if err != nil || len(second) != 3 || second[0].CPUMillicores != 3 {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	all, err := f.tenant.ListNodeMetrics(ctx, string(f.ref.RunID), string(f.ref.NodeID))
	if err != nil || len(all) != store.MaxNodeMetricSamples+8 {
		t.Fatalf("every sample = %d, %v", len(all), err)
	}
}
