package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestProfileUpgradeClearsPersistedLearningAndPreservesPins(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProfilePin(t.Context(), "parcels", "sort", 7, 8192); err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().Exec(`UPDATE pipeline_profiles SET
 p50_duration_ms=11,p99_duration_ms=22,peak_cores=3,peak_memory_bytes=4096,
 sample_count=5,cpu_measured=1,samples_json='{"schema":4,"samples":[]}',
 floor_cores=6,floor_memory_bytes=2048,prev_peak_cores=8,prev_peak_memory_bytes=1024,
 sustained_cores=2,prev_sustained_cores=4
 WHERE pipeline='parcels' AND node_id='sort'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version>=50`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	var rows int
	err = up.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pipeline_profiles WHERE
 pipeline='parcels' AND node_id='sort' AND pinned_cores=7 AND pinned_memory_bytes=8192
 AND p50_duration_ms=0 AND p99_duration_ms=0 AND peak_cores=0 AND peak_memory_bytes=0
 AND sample_count=0 AND cpu_measured=0 AND samples_json IS NULL
 AND floor_cores=0 AND floor_memory_bytes=0 AND prev_peak_cores=0 AND prev_peak_memory_bytes=0
 AND sustained_cores IS NULL AND prev_sustained_cores IS NULL`).Scan(&rows)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatal("migration did not clear persisted learning while preserving the pinned row")
	}
}
