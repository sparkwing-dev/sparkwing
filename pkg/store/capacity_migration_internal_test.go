package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteCompletedMigrationPreservesFreshProfiles(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if err := s.UpsertProfilePin(ctx, "parcels", "sort", 7, 8192); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProfileObservation(ctx, "parcels", "sort", ProfileObservation{
		Duration: time.Second, PeakCores: 2, SustainedCores: new(float64(0)),
		PeakMemoryBytes: 4096, CPUMeasured: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyVersionSQLite(ctx, 50); err != nil {
		t.Fatal(err)
	}
	profile, err := s.GetPipelineProfile(ctx, "parcels", "sort")
	if err != nil || profile == nil {
		t.Fatalf("profile=%+v error=%v", profile, err)
	}
	if profile.SampleCount != 1 || profile.P50Duration != time.Second || profile.PeakCores != 2 || profile.SustainedCores == nil || *profile.SustainedCores != 0 || profile.PeakMemoryBytes != 4096 || !profile.CPUMeasured || profile.PinnedCores != 7 || profile.PinnedMemoryBytes != 8192 {
		t.Fatalf("completed migration changed fresh profile: %+v", profile)
	}
}
