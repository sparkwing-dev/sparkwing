package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestRecordProfileObservation_IgnoresLegacySamples pins BW-652's version
// stamp: a profile row persisted in the pre-versioning bare-array format
// (whose durations folded in admission queue wait) is discarded on the
// next observation rather than contaminating the recomputed percentiles.
func TestRecordProfileObservation_IgnoresLegacySamples(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	legacy := `[{"d":19100000000,"c":3.0,"m":0},{"d":19000000000,"c":3.0,"m":0}]`
	if _, err := st.exec(ctx, `
INSERT INTO pipeline_profiles
    (pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, updated_at, samples_json)
VALUES (?, '', 19100, 19100, 3.0, 0, 2, ?, ?)`,
		"demo", time.Now().UnixNano(), legacy); err != nil {
		t.Fatal(err)
	}

	if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 3.0,
	}); err != nil {
		t.Fatalf("RecordProfileObservation: %v", err)
	}

	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1 (legacy samples discarded)", prof.SampleCount)
	}
	if prof.P50Duration != 10*time.Second {
		t.Errorf("P50Duration = %s, want 10s (only the clean observation counts)", prof.P50Duration)
	}
}
