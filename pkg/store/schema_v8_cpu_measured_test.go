package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaV8_UpgradePreservesRowsAndQualifiesLegacyPeaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema7.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.RecordProfileObservation(ctx, "legacy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 2, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed legacy profile: %v", err)
	}
	if err := st.RecordProfileObservation(ctx, "legacy", "node-a", store.ProfileObservation{
		Duration: time.Second, PeakCores: 1, PeakMemoryBytes: 512 << 20, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed legacy node profile: %v", err)
	}
	if err := st.RecordProfileObservation(ctx, "zero-peak", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 0, PeakMemoryBytes: 128 << 20, CPUMeasured: false,
	}); err != nil {
		t.Fatalf("seed zero-peak profile: %v", err)
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
	all, err := up.ListPipelineProfiles(ctx, "")
	if err != nil {
		t.Fatalf("list profiles after upgrade: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("profiles after upgrade = %d, want all 3 seeded rows to survive", len(all))
	}
	legacy, err := up.GetPipelineProfile(ctx, "legacy", "")
	if err != nil || legacy == nil {
		t.Fatalf("legacy rollup missing after upgrade: %v", err)
	}
	if legacy.PeakCores <= 0 || legacy.SampleCount != 1 {
		t.Errorf("legacy rollup lost data: %+v", legacy)
	}
	if !legacy.CPUMeasured {
		t.Error("legacy rollup with positive peak should qualify as cpu_measured after upgrade")
	}
	zero, err := up.GetPipelineProfile(ctx, "zero-peak", "")
	if err != nil || zero == nil {
		t.Fatalf("zero-peak rollup missing after upgrade: %v", err)
	}
	if zero.CPUMeasured {
		t.Error("zero-peak row must stay conservatively unmeasured after upgrade")
	}
}

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
