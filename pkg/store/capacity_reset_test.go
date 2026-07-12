package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestResetPipelineProfile_DropsLearnedRowsAndReLearns(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, obs := range []store.ProfileObservation{
		{Duration: 10 * time.Second, PeakCores: 2.0, PeakMemoryBytes: 1 << 30, CPUMeasured: true},
		{Duration: 20 * time.Second, PeakCores: 4.0, PeakMemoryBytes: 2 << 30, CPUMeasured: true},
		{Duration: 30 * time.Second, PeakCores: 99.0, PeakMemoryBytes: 40 << 30, CPUMeasured: true},
	} {
		if err := st.RecordProfileObservation(ctx, "demo", "", obs); err != nil {
			t.Fatalf("RecordProfileObservation: %v", err)
		}
	}
	if err := st.RecordProfileObservation(ctx, "other", "", store.ProfileObservation{Duration: time.Second, PeakCores: 1, CPUMeasured: true}); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	summary, err := st.ResetPipelineProfile(ctx, "demo")
	if err != nil {
		t.Fatalf("ResetPipelineProfile: %v", err)
	}
	if summary.RowsDeleted != 1 || summary.RowsCleared != 0 {
		t.Errorf("summary rows = deleted %d cleared %d, want deleted 1 cleared 0", summary.RowsDeleted, summary.RowsCleared)
	}
	if summary.SamplesDropped != 3 {
		t.Errorf("SamplesDropped = %d, want 3", summary.SamplesDropped)
	}
	if len(summary.Pipelines) != 1 || summary.Pipelines[0] != "demo" {
		t.Errorf("Pipelines = %v, want [demo]", summary.Pipelines)
	}

	gone, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if gone != nil {
		t.Errorf("demo profile should be gone after reset, got %+v", gone)
	}
	untouched, err := st.GetPipelineProfile(ctx, "other", "")
	if err != nil {
		t.Fatal(err)
	}
	if untouched == nil || untouched.SampleCount != 1 {
		t.Errorf("reset of demo must not touch other: %+v", untouched)
	}

	if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{Duration: 5 * time.Second, PeakCores: 3, CPUMeasured: true}); err != nil {
		t.Fatalf("re-learn: %v", err)
	}
	relearned, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if relearned == nil || relearned.SampleCount != 1 {
		t.Errorf("demo should re-learn from cold start, got %+v", relearned)
	}
	if relearned.PeakCores != 3 {
		t.Errorf("re-learned peak = %v, want 3 (not the old poisoned 99)", relearned.PeakCores)
	}
}

func TestResetPipelineProfile_KeepsPin(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if err := st.RecordProfileObservation(ctx, "pinned", "", store.ProfileObservation{Duration: 10 * time.Second, PeakCores: 50, PeakMemoryBytes: 8 << 30, CPUMeasured: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SetProfilePin(ctx, "pinned", "", 4, 2<<30); err != nil {
		t.Fatalf("SetProfilePin: %v", err)
	}

	summary, err := st.ResetPipelineProfile(ctx, "pinned")
	if err != nil {
		t.Fatalf("ResetPipelineProfile: %v", err)
	}
	if summary.RowsCleared != 1 || summary.RowsDeleted != 0 {
		t.Errorf("summary rows = deleted %d cleared %d, want deleted 0 cleared 1", summary.RowsDeleted, summary.RowsCleared)
	}

	prof, err := st.GetPipelineProfile(ctx, "pinned", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof == nil {
		t.Fatal("pinned row should survive reset")
	}
	if prof.PinnedCores != 4 || prof.PinnedMemoryBytes != 2<<30 {
		t.Errorf("pin not preserved: cores=%v mem=%d", prof.PinnedCores, prof.PinnedMemoryBytes)
	}
	if prof.SampleCount != 0 || prof.PeakCores != 0 {
		t.Errorf("learned data should be cleared: samples=%d peak=%v", prof.SampleCount, prof.PeakCores)
	}
}

func TestResetAllProfiles_ClearsEverythingKeepingPins(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"a", "b", "c"} {
		if err := st.RecordProfileObservation(ctx, name, "", store.ProfileObservation{Duration: time.Second, PeakCores: 2, CPUMeasured: true}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := st.SetProfilePin(ctx, "b", "", 3, 0); err != nil {
		t.Fatalf("pin b: %v", err)
	}

	summary, err := st.ResetAllProfiles(ctx)
	if err != nil {
		t.Fatalf("ResetAllProfiles: %v", err)
	}
	if summary.RowsDeleted != 2 || summary.RowsCleared != 1 {
		t.Errorf("summary rows = deleted %d cleared %d, want deleted 2 cleared 1", summary.RowsDeleted, summary.RowsCleared)
	}
	if len(summary.Pipelines) != 3 {
		t.Errorf("Pipelines = %v, want all three", summary.Pipelines)
	}

	all, err := st.ListPipelineProfiles(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Pipeline != "b" {
		t.Fatalf("only the pinned row should remain, got %+v", all)
	}
	if all[0].SampleCount != 0 || all[0].PinnedCores != 3 {
		t.Errorf("b should be cleared but keep its pin: %+v", all[0])
	}
}

func TestResetPipelineProfile_NoProfileIsNoOp(t *testing.T) {
	st := openTestStore(t)
	summary, err := st.ResetPipelineProfile(context.Background(), "absent")
	if err != nil {
		t.Fatalf("ResetPipelineProfile: %v", err)
	}
	if summary.RowsDeleted != 0 || summary.RowsCleared != 0 || summary.SamplesDropped != 0 {
		t.Errorf("resetting an absent pipeline should report zero counts, got %+v", summary)
	}
}
