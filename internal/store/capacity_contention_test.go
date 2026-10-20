package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestRecordContention_IncrementsRollupCount(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	if err := tenantFor(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{CPUMeasured: true, Duration: 10 * time.Second}); err != nil {
		t.Fatalf("RecordProfileObservation: %v", err)
	}
	for range 2 {
		if err := tenantFor(t, st, store.DefaultTeam).RecordContention(ctx, "demo"); err != nil {
			t.Fatalf("RecordContention: %v", err)
		}
	}

	prof, err := tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("GetPipelineProfile: %v (prof=%v)", err, prof)
	}
	if prof.ContendedCount != 2 {
		t.Errorf("ContendedCount = %d, want 2", prof.ContendedCount)
	}
}

func TestRecordContention_NoProfileIsNoOp(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := tenantFor(t, st, store.DefaultTeam).RecordContention(ctx, "never-run"); err != nil {
		t.Fatalf("RecordContention on absent profile should be a no-op, got %v", err)
	}
	prof, err := tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "never-run", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof != nil {
		t.Errorf("expected no profile row, got %+v", prof)
	}
}
