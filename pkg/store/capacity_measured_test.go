package store_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestProfileObservationRequiresMeasuredCPU(t *testing.T) {
	for _, node := range []string{"", "build"} {
		t.Run(node, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := t.Context()
			measured := store.ProfileObservation{CPUMeasured: true, Duration: time.Second, PeakCores: 2, SustainedCores: 1, PeakMemoryBytes: 100, PlanHash: "known"}
			for range 3 {
				if err := st.RecordProfileObservation(ctx, "known", node, measured); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.UpsertProfilePin(ctx, "known", node, 3, 200); err != nil {
				t.Fatal(err)
			}
			if node == "" {
				if err := st.RecordWaitObservation(ctx, "known", 5*time.Millisecond); err != nil {
					t.Fatal(err)
				}
			}
			before, err := st.GetPipelineProfile(ctx, "known", node)
			if err != nil {
				t.Fatal(err)
			}
			for _, contended := range []bool{false, true} {
				for _, hash := range []string{"known", "changed"} {
					unknown := store.ProfileObservation{Duration: 10 * time.Second, PeakCores: 50, SustainedCores: 40, PeakMemoryBytes: 10000, FloorCores: 50, FloorMemoryBytes: 10000, Contended: contended, PlanHash: hash}
					if err := st.RecordProfileObservation(ctx, "known", node, unknown); err != nil {
						t.Fatal(err)
					}
					after, err := st.GetPipelineProfile(ctx, "known", node)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, after) {
						t.Fatalf("unknown observation changed profile (contended=%t, hash=%s): before=%+v after=%+v", contended, hash, before, after)
					}
				}
			}
			measured.PlanHash = "next"
			measured.Contended = true
			measured.FloorCores = 1
			measured.FloorMemoryBytes = 50
			if err := st.RecordProfileObservation(ctx, "known", node, measured); err != nil {
				t.Fatal(err)
			}
			next, err := st.GetPipelineProfile(ctx, "known", node)
			if err != nil || next == nil || next.SampleCount != 0 || next.FloorCores != 1 || next.FloorMemoryBytes != 50 || !next.CPUMeasured || next.PrevPeakCores != 2 || next.PrevSustainedCores != 1 || next.PrevPeakMemoryBytes != 100 || next.PinnedCores != 3 || next.PinnedMemoryBytes != 200 {
				t.Fatalf("measured predecessor carry/pin lost: %+v,%v", next, err)
			}
			measured.CPUMeasured = false
			measured.PlanHash = "unmeasured"
			if err := st.RecordProfileObservation(ctx, "known", node, measured); err != nil {
				t.Fatal(err)
			}
			after, err := st.GetPipelineProfile(ctx, "known", node)
			if err != nil || !reflect.DeepEqual(next, after) {
				t.Fatalf("unknown observation changed measured carry/floor: before=%+v after=%+v err=%v", next, after, err)
			}
		})
	}
}

func TestUnknownObservationsCannotEnterLaterMeasuredWindow(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	unknown := store.ProfileObservation{Duration: 10 * time.Second, PeakCores: 50, PeakMemoryBytes: 10000, PlanHash: "unknown"}
	for range 3 {
		if err := st.RecordProfileObservation(ctx, "fresh", "", unknown); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := st.GetPipelineProfile(ctx, "fresh", ""); err != nil || p != nil {
		t.Fatalf("unknown observations created profile: %+v,%v", p, err)
	}
	if err := st.RecordProfileObservation(ctx, "fresh", "", store.ProfileObservation{CPUMeasured: true, Duration: time.Second, PeakMemoryBytes: 100, PlanHash: "measured"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPipelineProfile(ctx, "fresh", "")
	if err != nil || p == nil || p.SampleCount != 1 || !p.CPUMeasured || p.PeakCores != 0 || p.PeakMemoryBytes != 100 || p.P50Duration != time.Second || p.PrevPeakCores != 0 || p.PrevPeakMemoryBytes != 0 {
		t.Fatalf("unknown observation entered measured window: %+v,%v", p, err)
	}
}
