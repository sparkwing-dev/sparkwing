package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMetricTimestampConflicts(t *testing.T) {
	for _, field := range []string{"kind", "CPU rate", "memory", "CPU time"} {
		t.Run(field, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
			a := metricAuthorization(t, f, "metrics")
			original := store.MetricSample{Kind: store.MetricCommand, TS: time.Unix(10, 123), CPUMillicores: 250, MemoryBytes: 4096, CPUTime: time.Second}
			if err := f.tenant.WriteMetric(t.Context(), a, original); err != nil {
				t.Fatal(err)
			}
			if err := f.tenant.WriteMetric(t.Context(), a, original); err != nil {
				t.Fatalf("identical retry: %v", err)
			}
			zoned := original
			zoned.TS = original.TS.In(time.FixedZone("offset", 3600))
			if err := f.tenant.WriteMetric(t.Context(), a, zoned); err != nil {
				t.Fatalf("same instant in another timezone: %v", err)
			}
			conflict := original
			switch field {
			case "kind":
				conflict.Kind = store.MetricUnknown
			case "CPU rate":
				conflict.CPUMillicores++
			case "memory":
				conflict.MemoryBytes++
			case "CPU time":
				conflict.CPUTime++
			}
			err := f.tenant.WriteMetric(t.Context(), a, conflict)
			if err == nil || !strings.Contains(err.Error(), "conflicting node metric") {
				t.Fatalf("conflicting sample was not rejected: %v", err)
			}
			got, err := f.tenant.ListNodeMetrics(t.Context(), f.ref.RunID, f.ref.NodeID)
			if err != nil || len(got) != 1 {
				t.Fatalf("stored samples = %v, %v", got, err)
			}
			if got[0].Kind != original.Kind || got[0].CPUMillicores != original.CPUMillicores || got[0].MemoryBytes != original.MemoryBytes || got[0].CPUTime != original.CPUTime || !got[0].TS.Equal(original.TS) {
				t.Fatalf("conflict changed original sample: %+v", got[0])
			}
		})
	}
}

func TestMetricTimestampConcurrentWriters(t *testing.T) {
	for _, conflicting := range []bool{false, true} {
		name := "identical"
		if conflicting {
			name = "conflicting"
		}
		t.Run(name, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
			a := metricAuthorization(t, f, "metrics")
			samples := [2]store.MetricSample{
				{Kind: store.MetricInterval, TS: time.Unix(10, 123), MemoryBytes: 4096},
				{Kind: store.MetricInterval, TS: time.Unix(10, 123), MemoryBytes: 4096},
			}
			if conflicting {
				samples[1].Kind = store.MetricUnknown
			}
			start := make(chan struct{})
			type writeResult struct {
				index int
				err   error
			}
			results := make(chan writeResult, 2)
			for index, sample := range samples {
				go func() {
					<-start
					results <- writeResult{index, f.tenant.WriteMetric(t.Context(), a, sample)}
				}()
			}
			close(start)
			failures := 0
			var acknowledged []store.MetricSample
			for range samples {
				result := <-results
				if err := result.err; err != nil {
					if !strings.Contains(err.Error(), "conflicting node metric") {
						t.Errorf("unexpected write error: %v", err)
					}
					failures++
				} else {
					acknowledged = append(acknowledged, samples[result.index])
				}
			}
			wantFailures := 0
			if conflicting {
				wantFailures = 1
			}
			if failures != wantFailures {
				t.Fatalf("write failures = %d; want %d", failures, wantFailures)
			}
			got, err := f.tenant.ListNodeMetrics(t.Context(), f.ref.RunID, f.ref.NodeID)
			if err != nil || len(got) != 1 {
				t.Fatalf("stored samples = %v, %v", got, err)
			}
			for _, want := range acknowledged {
				if got[0].Kind != want.Kind || got[0].CPUMillicores != want.CPUMillicores || got[0].MemoryBytes != want.MemoryBytes || got[0].CPUTime != want.CPUTime || !got[0].TS.Equal(want.TS) {
					t.Fatalf("stored sample %+v differs from acknowledged sample %+v", got[0], want)
				}
			}
		})
	}
}
