package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDispatchContextCarriesTheClaimItRunsUnder(t *testing.T) {
	fence, ok := store.TriggerClaimFenceFromContext(
		DispatchContext(context.Background(), &store.Trigger{ID: "run-ctx", ClaimSeq: 7}))
	if !ok {
		t.Fatal("a dispatch context with no claim fence lets a superseded write land unrefused")
	}
	if fence.ClaimGeneration != 7 {
		t.Errorf("ClaimGeneration = %d, want 7", fence.ClaimGeneration)
	}
}
