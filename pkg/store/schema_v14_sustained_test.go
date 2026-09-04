package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestSchemaV14_UpgradeBackfillsSustainedFromPeak(t *testing.T) {
	target := storetest.New(t)

	st, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.RecordProfileObservation(ctx, "legacy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 3, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed legacy rollup: %v", err)
	}
	if err := st.RecordProfileObservation(ctx, "legacy", "node-a", store.ProfileObservation{
		Duration: time.Second, PeakCores: 2, PeakMemoryBytes: 512 << 20, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed legacy node row: %v", err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE pipeline_profiles DROP COLUMN sustained_cores`); err != nil {
		t.Fatalf("drop sustained_cores: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 14`); err != nil {
		t.Fatalf("reset version to 13: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	if v := readSchemaVersion(t, st.DB()); v != 13 {
		t.Fatalf("seeded version = %d, want 13", v)
	}
	if hasColumn(t, st, "pipeline_profiles", "sustained_cores") {
		t.Fatal("sustained_cores should be absent before upgrade")
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
	if !hasColumn(t, up, "pipeline_profiles", "sustained_cores") {
		t.Fatal("sustained_cores should be present after upgrade")
	}
	for _, tc := range []struct {
		node string
		want float64
	}{{"", 3}, {"node-a", 2}} {
		prof, err := up.GetPipelineProfile(ctx, "legacy", tc.node)
		if err != nil || prof == nil {
			t.Fatalf("legacy row %q missing after upgrade: %v", tc.node, err)
		}
		if prof.SustainedCores != tc.want {
			t.Errorf("row %q SustainedCores = %v, want its own peak %v", tc.node, prof.SustainedCores, tc.want)
		}
		if prof.PeakCores != tc.want {
			t.Errorf("row %q PeakCores = %v, want %v unchanged by the upgrade", tc.node, prof.PeakCores, tc.want)
		}
	}
}

func TestProfileWindow_SchemaThreeSamplesBackfillSustained(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.RecordProfileObservation(ctx, "legacy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 4, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"schema": 3,
		"samples": []map[string]any{
			{"d": int64(time.Second), "c": 4.0, "m": int64(1) << 30},
			{"d": int64(time.Second), "c": 4.0, "m": int64(1) << 30},
			{"d": int64(time.Second), "c": 4.0, "m": int64(1) << 30},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE pipeline_profiles SET samples_json = ?, sample_count = 3 WHERE pipeline = 'legacy' AND node_id = ''`),
		raw); err != nil {
		t.Fatalf("rewrite window as schema 3: %v", err)
	}

	if err := st.RecordProfileObservation(ctx, "legacy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("fold fresh observation: %v", err)
	}
	prof, err := st.GetPipelineProfile(ctx, "legacy", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.SampleCount != 4 {
		t.Fatalf("SampleCount = %d, want the 3 carried samples plus the fresh one", prof.SampleCount)
	}
	if prof.SustainedCores != 4 {
		t.Errorf("SustainedCores = %v, want 4 (carried samples priced at their peaks)", prof.SustainedCores)
	}
}

func TestProfileWindow_WriterWithoutSustainedStoresThePeak(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.RecordProfileObservation(ctx, "cluster", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 2, SustainedCores: 2, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed carried sample: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := st.RecordProfileObservation(ctx, "cluster", "", store.ProfileObservation{
			Duration: time.Second, PeakCores: 10, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
		}); err != nil {
			t.Fatalf("fold cluster observation %d: %v", i, err)
		}
	}

	prof, err := st.GetPipelineProfile(ctx, "cluster", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.SustainedCores != 10 {
		t.Errorf("SustainedCores = %v, want 10 (a sustained-less writer stores its peak)", prof.SustainedCores)
	}
	if prof.PeakCores != 10 {
		t.Errorf("PeakCores = %v, want 10", prof.PeakCores)
	}
}

func TestSchemaV14_UpgradeOfARealV13ShapeBackfillsWindowAndColumn(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.RecordProfileObservation(ctx, "legacy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 6, PeakMemoryBytes: 1 << 30, CPUMeasured: true, PlanHash: "A",
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"schema": 3,
		"samples": []map[string]any{
			{"d": int64(time.Second), "c": 6.0, "m": int64(1) << 30},
			{"d": int64(time.Second), "c": 6.0, "m": int64(1) << 30},
			{"d": int64(time.Second), "c": 6.0, "m": int64(1) << 30},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE pipeline_profiles SET samples_json = ?, sample_count = 3, peak_cores = 6, prev_peak_cores = 4`),
		raw); err != nil {
		t.Fatalf("write v13 window shape: %v", err)
	}
	for _, col := range []string{"sustained_cores", "prev_sustained_cores"} {
		if _, err := st.DB().Exec(`ALTER TABLE pipeline_profiles DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 14`); err != nil {
		t.Fatalf("reset version to 13: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	_ = st.Close()

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()

	prof, err := up.GetPipelineProfile(ctx, "legacy", "")
	if err != nil || prof == nil {
		t.Fatalf("legacy row missing after upgrade: %v", err)
	}
	if prof.SustainedCores != 6 {
		t.Errorf("SustainedCores = %v, want the carried 6.0 peak", prof.SustainedCores)
	}
	if prof.PrevSustainedCores != 4 {
		t.Errorf("PrevSustainedCores = %v, want the carried 4.0 prev peak", prof.PrevSustainedCores)
	}

	if err := up.RecordProfileObservation(ctx, "legacy", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 6, SustainedCores: 1, PeakMemoryBytes: 1 << 30,
		CPUMeasured: true, PlanHash: "A",
	}); err != nil {
		t.Fatalf("fold after upgrade: %v", err)
	}
	after, err := up.GetPipelineProfile(ctx, "legacy", "")
	if err != nil || after == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if after.SustainedCores != 6 {
		t.Errorf("SustainedCores = %v, want 6 (carried samples still priced at their peaks)", after.SustainedCores)
	}
}

func TestSchemaV14_BackfillIsSafeToReplay(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.RecordProfileObservation(ctx, "measured", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 6, SustainedCores: 2, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed measured row: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 14`); err != nil {
		t.Fatalf("rewind version stamp: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	_ = st.Close()

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2 (replay): %v", err)
	}
	defer func() { _ = up.Close() }()

	prof, err := up.GetPipelineProfile(ctx, "measured", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.SustainedCores != 2 {
		t.Errorf("SustainedCores = %v, want the measured 2.0 preserved across a replayed backfill", prof.SustainedCores)
	}
}

func TestRecordProfileObservation_PlanHashChangeCarriesSustained(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
			Duration: time.Second, PeakCores: 8, SustainedCores: 2,
			PeakMemoryBytes: 1 << 30, CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatalf("seed observation %d: %v", i, err)
		}
	}
	if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 1, SustainedCores: 1,
		PeakMemoryBytes: 1 << 30, CPUMeasured: true, PlanHash: "B",
	}); err != nil {
		t.Fatalf("fold structural change: %v", err)
	}

	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.PrevSustainedCores != 2 {
		t.Errorf("PrevSustainedCores = %v, want the predecessor's 2.0 charge", prof.PrevSustainedCores)
	}
	if prof.PrevPeakCores != 8 {
		t.Errorf("PrevPeakCores = %v, want the predecessor's 8.0 peak still carried", prof.PrevPeakCores)
	}
}

func TestProfileFromWindow_SustainedTakesTheSameAcrossRunRankAsThePeak(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for i := 0; i < 19; i++ {
		if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
			Duration: time.Second, PeakCores: 6, SustainedCores: 2, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
		}); err != nil {
			t.Fatalf("seed observation %d: %v", i, err)
		}
	}
	if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 40, SustainedCores: 30, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("seed freak run: %v", err)
	}

	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.SustainedCores != 2 {
		t.Errorf("SustainedCores = %v, want 2 (p95 across runs drops the freak run)", prof.SustainedCores)
	}
	if prof.PeakCores != 6 {
		t.Errorf("PeakCores = %v, want 6 unchanged by the sustained figure", prof.PeakCores)
	}
}

func TestProfileObservation_SustainedRoundTripsPerRun(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for i, sustained := range []float64{1, 5, 3} {
		if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
			Duration: time.Second, PeakCores: 9, SustainedCores: sustained,
			PeakMemoryBytes: 1 << 30, CPUMeasured: true,
		}); err != nil {
			t.Fatalf("seed observation %d: %v", i, err)
		}
	}
	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.SustainedCores != 5 {
		t.Errorf("SustainedCores = %v, want 5 (p95 of 1, 5, 3)", prof.SustainedCores)
	}
	if prof.PeakCores != 9 {
		t.Errorf("PeakCores = %v, want 9", prof.PeakCores)
	}
}

func TestNearestRankPercentile_MatchesTheRankProfilesAreBuiltOn(t *testing.T) {
	cases := []struct {
		xs   []float64
		q    float64
		want float64
	}{
		{nil, 0.8, 0},
		{[]float64{7}, 0.8, 7},
		{[]float64{1, 9}, 0.8, 9},
		{[]float64{1, 1, 1, 1, 9}, 0.8, 1},
		{[]float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 9}, 0.8, 1},
		{[]float64{1, 2, 3, 4, 5}, 0.8, 4},
		{[]float64{1, 2, 3}, 0.95, 3},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%v@%v", tc.xs, tc.q), func(t *testing.T) {
			if got := store.NearestRankPercentile(tc.xs, tc.q); got != tc.want {
				t.Errorf("NearestRankPercentile(%v, %v) = %v, want %v", tc.xs, tc.q, got, tc.want)
			}
		})
	}
}
