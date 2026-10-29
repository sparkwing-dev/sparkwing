package store_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestMetricIdentityPreservesTenantAndConflictingRetriesProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.1s measured in the short class; the full suite runs it")
	}
	f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
	foreign := tenantFor(t, f.s, "foreign")
	a := metricAuthorization(t, f, "metrics")
	random := rand.New(rand.NewPCG(981, 372))
	at := time.Unix(1800000000, 123)
	for i := range 64 {
		sample := store.MetricSample{TS: at.Add(time.Duration(i) * time.Nanosecond), CPUMillicores: random.Int64N(1 << 40), MemoryBytes: random.Int64N(1 << 60), CPUTime: time.Duration(random.Int64N(1 << 60))}
		if err := foreign.WriteMetric(t.Context(), a, sample); err == nil {
			t.Fatalf("foreign metric: %v", err)
		}
		for range 2 {
			if err := f.tenant.WriteMetric(t.Context(), a, sample); err != nil {
				t.Fatal(err)
			}
		}
		conflict := sample
		switch i % 3 {
		case 0:
			conflict.CPUMillicores++
		case 1:
			conflict.MemoryBytes++
		case 2:
			conflict.CPUTime++
		}
		if err := f.tenant.WriteMetric(t.Context(), a, conflict); err == nil {
			t.Fatal("conflicting metric replaced its predecessor")
		}
		rows, err := f.tenant.ListNodeMetrics(t.Context(), string(f.ref.RunID), string(f.ref.NodeID))
		if err != nil || len(rows) != i+1 {
			t.Fatalf("samples = %d, %v; want %d", len(rows), err, i+1)
		}
		got := rows[i]
		if !got.TS.Equal(sample.TS) || got.CPUMillicores != sample.CPUMillicores || got.MemoryBytes != sample.MemoryBytes || got.CPUTime != sample.CPUTime {
			t.Fatalf("sample changed: %+v; want %+v", got, sample)
		}
	}
	if rows, err := foreign.ListNodeMetrics(t.Context(), string(f.ref.RunID), string(f.ref.NodeID)); err != nil || len(rows) != 0 {
		t.Fatalf("foreign samples = %d, %v", len(rows), err)
	}
}
