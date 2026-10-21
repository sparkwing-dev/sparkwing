package store_test

import (
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAddNodeMetricSample_Validation(t *testing.T) {
	minimum := time.Unix(0, math.MinInt64)
	maximum := time.Unix(0, math.MaxInt64)
	for _, tc := range []struct {
		name   string
		sample store.MetricSample
		valid  bool
	}{
		{"minimum timestamp", store.MetricSample{TS: minimum}, true},
		{"maximum timestamp", store.MetricSample{TS: maximum}, true},
		{"epoch and zero usage", store.MetricSample{TS: time.Unix(0, 0)}, true},
		{"maximum usage", store.MetricSample{TS: time.Unix(1, 0), CPUMillicores: math.MaxInt64, MemoryBytes: math.MaxInt64, CPUTime: time.Duration(math.MaxInt64)}, true},
		{"below timestamp range", store.MetricSample{TS: minimum.Add(-time.Nanosecond)}, false},
		{"above timestamp range", store.MetricSample{TS: maximum.Add(time.Nanosecond)}, false},
		{"year 2500", store.MetricSample{TS: time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)}, false},
		{"unset timestamp", store.MetricSample{}, false},
		{"negative CPU rate", store.MetricSample{TS: time.Unix(1, 0), CPUMillicores: -1}, false},
		{"negative memory", store.MetricSample{TS: time.Unix(1, 0), MemoryBytes: -1}, false},
		{"negative CPU time", store.MetricSample{TS: time.Unix(1, 0), CPUTime: -1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
			a := metricAuthorization(t, f, "metrics")
			ctx := t.Context()
			err := f.tenant.WriteMetric(ctx, a, tc.sample)
			if (err == nil) != tc.valid {
				t.Errorf("AddNodeMetricSample error = %v; valid = %v", err, tc.valid)
			}
			rows, err := f.tenant.ListNodeMetrics(ctx, string(f.ref.RunID), string(f.ref.NodeID))
			if err != nil {
				t.Fatal(err)
			}
			if !tc.valid {
				if len(rows) != 0 {
					t.Errorf("invalid sample persisted: %+v", rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("samples = %d, want 1", len(rows))
			}
			got := rows[0]
			if !got.TS.Equal(tc.sample.TS) || got.CPUMillicores != tc.sample.CPUMillicores || got.MemoryBytes != tc.sample.MemoryBytes || got.CPUTime != tc.sample.CPUTime {
				t.Errorf("stored sample = %+v, want %+v", got, tc.sample)
			}
		})
	}
}
