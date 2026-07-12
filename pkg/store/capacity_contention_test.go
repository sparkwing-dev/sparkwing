package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordContention_IncrementsRollupCount(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{Duration: 10 * time.Second}); err != nil {
		t.Fatalf("RecordProfileObservation: %v", err)
	}
	for range 2 {
		if err := st.RecordContention(ctx, "demo"); err != nil {
			t.Fatalf("RecordContention: %v", err)
		}
	}

	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("GetPipelineProfile: %v (prof=%v)", err, prof)
	}
	if prof.ContendedCount != 2 {
		t.Errorf("ContendedCount = %d, want 2", prof.ContendedCount)
	}
}

func TestRecordContention_NoProfileIsNoOp(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.RecordContention(ctx, "never-run"); err != nil {
		t.Fatalf("RecordContention on absent profile should be a no-op, got %v", err)
	}
	prof, err := st.GetPipelineProfile(ctx, "never-run", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof != nil {
		t.Errorf("expected no profile row, got %+v", prof)
	}
}
