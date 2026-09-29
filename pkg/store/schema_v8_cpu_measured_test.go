package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSchemaV8_UpgradePreservesRowsAndInvalidatesIncompatibleLearning(t *testing.T) {
	target := storetest.New(t)

	st, err := target.TryOpen()
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
	deleteFleetRequirements(t, st.DB())
	if v := readSchemaVersion(t, st.DB()); v != 7 {
		t.Fatalf("seeded version = %d, want 7", v)
	}
	if hasColumn(t, st, "pipeline_profiles", "cpu_measured") {
		t.Fatal("cpu_measured should be absent before upgrade")
	}
	_ = st.Close()

	up, err := target.TryOpen()
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
	for _, profile := range all {
		if profile.PeakCores != 0 || profile.PeakMemoryBytes != 0 || profile.SampleCount != 0 || profile.CPUMeasured || profile.SustainedCores != nil {
			t.Errorf("incompatible learning survived upgrade: %+v", profile)
		}
	}

}

func TestPipelineProfile_CPUMeasuredRoundTrips(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
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
