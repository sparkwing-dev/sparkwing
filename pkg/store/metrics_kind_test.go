package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMetricKindsRoundTrip(t *testing.T) {
	st := storetest.New(t).Open(t)
	readyNode(t, st, "kind-run", "node")
	samples := []store.MetricSample{
		{Kind: store.MetricInterval, TS: time.Unix(1, 0), MemoryBytes: 200},
		{Kind: store.MetricCommand, TS: time.Unix(2, 0), MemoryBytes: 400},
		{Kind: store.MetricUnknown, TS: time.Unix(3, 0), MemoryBytes: 600},
		{Kind: store.MetricCommand, TS: time.Unix(4, 0), CPUTime: time.Second, CPUMillicores: 500},
	}
	for _, sample := range samples {
		if err := st.AddNodeMetricSample(t.Context(), "kind-run", "node", sample); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListNodeMetrics(t.Context(), "kind-run", "node")
	if err != nil || len(got) != len(samples) {
		t.Fatalf("samples = %v, %v", got, err)
	}
	for i, want := range samples {
		if got[i].Kind != want.Kind || got[i].CPUTime != want.CPUTime || got[i].MemoryBytes != want.MemoryBytes || got[i].CPUMillicores != want.CPUMillicores || !got[i].TS.Equal(want.TS) {
			t.Fatalf("sample %d = %+v; want %+v", i, got[i], want)
		}
	}
}

func TestMetricKindsRejectInvalidCombinations(t *testing.T) {
	st := storetest.New(t).Open(t)
	readyNode(t, st, "kind-run", "node")
	for _, sample := range []store.MetricSample{
		{Kind: "future", TS: time.Unix(1, 0)},
		{Kind: store.MetricInterval, CPUTime: time.Second, TS: time.Unix(2, 0)},
	} {
		if err := st.AddNodeMetricSample(t.Context(), "kind-run", "node", sample); err == nil {
			t.Fatalf("accepted %+v", sample)
		}
	}
	got, err := st.ListNodeMetrics(t.Context(), "kind-run", "node")
	if err != nil || len(got) != 0 {
		t.Fatalf("invalid samples persisted: %v, %v", got, err)
	}
}

func TestMetricKindsMigrateOnlyKnownCommands(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	readyNode(t, st, "kind-run", "node")
	for _, query := range []string{
		`INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos) VALUES ('kind-run', 'node', 1, 9000, 400, 1000000000)`,
		`INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos) VALUES ('kind-run', 'node', 2, 0, 200, 0)`,
		`ALTER TABLE node_metrics DROP COLUMN kind`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 50`,
		`DELETE FROM sparkwing_requirements WHERE name = 'metric-sample-kind'`,
	} {
		if _, err := st.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up := target.Open(t)
	got, err := up.ListNodeMetrics(t.Context(), "kind-run", "node")
	if err != nil || len(got) != 2 {
		t.Fatalf("samples = %v, %v", got, err)
	}
	if got[0].Kind != store.MetricCommand || got[1].Kind != store.MetricUnknown {
		t.Fatalf("historical kinds = %q, %q; want command, unknown", got[0].Kind, got[1].Kind)
	}
	if got[0].CPUTime != time.Second || got[0].MemoryBytes != 400 || got[1].MemoryBytes != 200 {
		t.Fatalf("migration altered measurements: %+v", got)
	}
}
