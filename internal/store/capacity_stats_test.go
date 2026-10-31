package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPipelineProfile_ResourcePercentilesFromSamples(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for i := 1; i <= 10; i++ {
		if err := tenantFor(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "spiky", "", store.ProfileObservation{
			Duration:        time.Minute,
			PeakCores:       float64(i),
			PeakMemoryBytes: int64(i) << 20,
			CPUMeasured:     true,
		}); err != nil {
			t.Fatalf("record observation %d: %v", i, err)
		}
	}
	prof, err := tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "spiky", "")
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

	list, err := tenantFor(t, st, store.DefaultTeam).ListPipelineProfiles(ctx, "spiky")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v (n=%d)", err, len(list))
	}
	if list[0].CPUP50 != prof.CPUP50 || list[0].MemoryP95Bytes != prof.MemoryP95Bytes {
		t.Errorf("List and Get disagree on percentiles: %+v vs %+v", list[0], prof)
	}
}

func TestRecordProfileObservation_DropsPreviousCPUSampleSchema(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	oldWindow, err := json.Marshal(map[string]any{
		"schema": 2,
		"samples": []map[string]any{
			{"d": int64(time.Minute), "c": 14.0, "m": int64(1 << 30)},
			{"d": int64(time.Minute), "c": 14.0, "m": int64(1 << 30)},
			{"d": int64(time.Minute), "c": 14.0, "m": int64(1 << 30)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st, `
INSERT INTO pipeline_profiles
    (team, pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, cpu_measured, updated_at, samples_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		store.DefaultTeam, "schema-shift", "", time.Minute.Milliseconds(), time.Minute.Milliseconds(), 14.0, int64(1<<30), 3, 1,
		time.Now().UnixNano(), oldWindow); err != nil {
		t.Fatalf("seed old profile window: %v", err)
	}

	prof, err := tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "schema-shift", "")
	if err != nil || prof == nil {
		t.Fatalf("get stale profile: %v (prof=%v)", err, prof)
	}
	if prof.SampleCount != 0 {
		t.Errorf("stale SampleCount = %d, want old CPU sample schema ignored on read", prof.SampleCount)
	}
	if prof.PeakCores != 0 {
		t.Errorf("stale PeakCores = %v, want old CPU sample schema ignored on read", prof.PeakCores)
	}
	if got := capacity.Resolve(prof, 12, ""); got.Source != store.CostSourceDefault {
		t.Errorf("stale profile resolved as %q, want default", got.Source)
	}

	if err := tenantFor(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "schema-shift", "", store.ProfileObservation{
		Duration:        5 * time.Second,
		PeakCores:       2,
		PeakMemoryBytes: 128 << 20,
		CPUMeasured:     true,
	}); err != nil {
		t.Fatalf("RecordProfileObservation: %v", err)
	}

	prof, err = tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "schema-shift", "")
	if err != nil || prof == nil {
		t.Fatalf("get profile: %v (prof=%v)", err, prof)
	}
	if prof.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want old CPU sample schema dropped", prof.SampleCount)
	}
	if prof.PeakCores != 2 {
		t.Errorf("PeakCores = %v, want 2 from the new observation only", prof.PeakCores)
	}
}
