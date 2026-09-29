package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMetricAvailabilityUpgradePreservesUnknownHistory(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	start := time.Unix(100, 0).UTC()
	if err := st.CreateRun(ctx, store.Run{ID: "r", Pipeline: "parcels", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r", NodeID: "sort", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "r", "sort", store.MetricSample{TS: start, CPUMillicores: 750, MemoryBytes: 4096, CPUTime: time.Second}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE node_metrics DROP COLUMN sample_kind`,
		`ALTER TABLE node_metrics DROP COLUMN cpu_available`,
		`ALTER TABLE node_metrics DROP COLUMN memory_available`,
		`DELETE FROM sparkwing_schema_version WHERE version>=51`,
	} {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	var count int
	if err := up.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_metrics WHERE run_id='r' AND node_id='sort' AND sample_kind='' AND cpu_available=0 AND memory_available=0 AND cpu_millicores=750 AND memory_bytes=4096 AND cpu_time_nanos=1000000000`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("migration changed historical quantities or claimed availability")
	}
	got, err := up.ListNodeMetrics(ctx, "r", "sort")
	if err != nil || len(got) != 1 {
		t.Fatalf("history=%+v, error=%v", got, err)
	}
	if got[0].Kind != "" || got[0].CPUAvailable || got[0].MemoryAvailable || got[0].OneShot() {
		t.Fatalf("historical evidence was reclassified: %+v", got[0])
	}
}
