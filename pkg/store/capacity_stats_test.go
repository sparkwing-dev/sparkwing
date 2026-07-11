package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordWaitObservation_PersistsWindowedPercentiles(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "waits.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for _, w := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 100 * time.Second} {
		if err := st.RecordWaitObservation(ctx, "queuey", w); err != nil {
			t.Fatalf("record wait: %v", err)
		}
	}
	prof, err := st.GetPipelineProfile(ctx, "queuey", "")
	if err != nil || prof == nil {
		t.Fatalf("get profile: %v (prof=%v)", err, prof)
	}
	if prof.WaitSampleCount != 5 {
		t.Errorf("WaitSampleCount = %d, want 5", prof.WaitSampleCount)
	}
	if prof.WaitP50 != 3*time.Second {
		t.Errorf("WaitP50 = %s, want 3s (nearest-rank median of 1,2,3,4,100s)", prof.WaitP50)
	}
	if prof.WaitP99 != 100*time.Second {
		t.Errorf("WaitP99 = %s, want 100s", prof.WaitP99)
	}
}

func TestRecordWaitObservation_AgesOutOldSamples(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "waits.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for range 60 {
		if err := st.RecordWaitObservation(ctx, "aged", time.Hour); err != nil {
			t.Fatalf("record old wait: %v", err)
		}
	}
	for range 50 {
		if err := st.RecordWaitObservation(ctx, "aged", time.Second); err != nil {
			t.Fatalf("record new wait: %v", err)
		}
	}
	prof, err := st.GetPipelineProfile(ctx, "aged", "")
	if err != nil || prof == nil {
		t.Fatalf("get profile: %v", err)
	}
	if prof.WaitSampleCount != 50 {
		t.Errorf("WaitSampleCount = %d, want window cap 50", prof.WaitSampleCount)
	}
	if prof.WaitP99 != time.Second {
		t.Errorf("WaitP99 = %s, want 1s once hour-long waits aged out", prof.WaitP99)
	}
}

// TestRecordWaitObservation_CoexistsWithProfileUpserts pins that the wait
// upsert never clobbers measured profile columns and vice versa,
// whichever lands first.
func TestRecordWaitObservation_CoexistsWithProfileUpserts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "waits.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.RecordWaitObservation(ctx, "mixed", 7*time.Second); err != nil {
		t.Fatalf("record wait first: %v", err)
	}
	if err := st.RecordProfileObservation(ctx, "mixed", "", store.ProfileObservation{
		Duration: time.Minute, PeakCores: 2, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("record profile after wait: %v", err)
	}
	if err := st.RecordWaitObservation(ctx, "mixed", 9*time.Second); err != nil {
		t.Fatalf("record wait after profile: %v", err)
	}

	prof, err := st.GetPipelineProfile(ctx, "mixed", "")
	if err != nil || prof == nil {
		t.Fatalf("get profile: %v", err)
	}
	if prof.SampleCount != 1 || prof.PeakCores != 2 {
		t.Errorf("profile columns lost across wait upserts: %+v", prof)
	}
	if prof.WaitSampleCount != 2 || prof.WaitP99 != 9*time.Second {
		t.Errorf("wait columns lost across profile upserts: %+v", prof)
	}
}

func TestPipelineProfile_ResourcePercentilesFromSamples(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for i := 1; i <= 10; i++ {
		if err := st.RecordProfileObservation(ctx, "spiky", "", store.ProfileObservation{
			Duration:        time.Minute,
			PeakCores:       float64(i),
			PeakMemoryBytes: int64(i) << 20,
			CPUMeasured:     true,
		}); err != nil {
			t.Fatalf("record observation %d: %v", i, err)
		}
	}
	prof, err := st.GetPipelineProfile(ctx, "spiky", "")
	if err != nil || prof == nil {
		t.Fatalf("get profile: %v", err)
	}
	if prof.CPUP50 != 5 {
		t.Errorf("CPUP50 = %v, want 5 (nearest-rank median of 1..10)", prof.CPUP50)
	}
	if prof.CPUP95 != 10 {
		t.Errorf("CPUP95 = %v, want 10", prof.CPUP95)
	}
	if prof.MemoryP50Bytes != 5<<20 {
		t.Errorf("MemoryP50Bytes = %v, want %v", prof.MemoryP50Bytes, 5<<20)
	}
	if prof.MemoryP95Bytes != 10<<20 {
		t.Errorf("MemoryP95Bytes = %v, want %v", prof.MemoryP95Bytes, 10<<20)
	}
	if prof.PeakCores != 10 {
		t.Errorf("PeakCores = %v, want windowed p99 peak 10", prof.PeakCores)
	}

	list, err := st.ListPipelineProfiles(ctx, "spiky")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v (n=%d)", err, len(list))
	}
	if list[0].CPUP50 != prof.CPUP50 || list[0].MemoryP95Bytes != prof.MemoryP95Bytes {
		t.Errorf("List and Get disagree on percentiles: %+v vs %+v", list[0], prof)
	}
}
