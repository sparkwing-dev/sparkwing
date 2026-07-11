package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestSchemaV8_UpgradeAddsCPUMeasuredColumn reconstructs a schema-7
// pipeline_profiles store without the cpu_measured column, then opens it
// with the current binary and asserts the v8 migration adds the column and
// leaves existing rows conservative (cpu_measured false).
func TestSchemaV8_UpgradeAddsCPUMeasuredColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema7.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	if err := st.RecordProfileObservation(context.Background(), "legacy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 2, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE pipeline_profiles DROP COLUMN cpu_measured`); err != nil {
		t.Fatalf("drop cpu_measured: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 8`); err != nil {
		t.Fatalf("reset version to 7: %v", err)
	}
	if v := readSchemaVersion(t, st.DB()); v != 7 {
		t.Fatalf("seeded version = %d, want 7", v)
	}
	if hasColumn(t, st, "pipeline_profiles", "cpu_measured") {
		t.Fatal("cpu_measured should be absent before upgrade")
	}
	_ = st.Close()

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()

	if v := readSchemaVersion(t, up.DB()); v != store.ExpectedSchemaVersion() {
		t.Errorf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	if !hasColumn(t, up, "pipeline_profiles", "cpu_measured") {
		t.Fatal("cpu_measured should be present after upgrade")
	}
	prof, err := up.GetPipelineProfile(context.Background(), "legacy", "")
	if err != nil {
		t.Fatalf("get legacy profile: %v", err)
	}
	if prof == nil {
		t.Fatal("legacy profile missing after upgrade")
	}
	if prof.CPUMeasured {
		t.Error("legacy row backfilled to cpu_measured=true; want conservative false")
	}
}

// TestPipelineProfile_CPUMeasuredRoundTrips records observations with the
// cpu_measured bit set and clear and confirms the stored profile reflects
// the latest observation's value.
func TestPipelineProfile_CPUMeasuredRoundTrips(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.RecordProfileObservation(ctx, "healthy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 0, PeakMemoryBytes: 128 << 20, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("record healthy: %v", err)
	}
	healthy, err := st.GetPipelineProfile(ctx, "healthy", "")
	if err != nil {
		t.Fatalf("get healthy: %v", err)
	}
	if !healthy.CPUMeasured {
		t.Error("healthy sampler observation did not persist cpu_measured=true")
	}

	if err := st.RecordProfileObservation(ctx, "blind", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 0, PeakMemoryBytes: 128 << 20, CPUMeasured: false,
	}); err != nil {
		t.Fatalf("record blind: %v", err)
	}
	blind, err := st.GetPipelineProfile(ctx, "blind", "")
	if err != nil {
		t.Fatalf("get blind: %v", err)
	}
	if blind.CPUMeasured {
		t.Error("blind sampler observation persisted cpu_measured=true")
	}
}
