package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestController_QueueStateViewUnifiesKeys proves the controller serves its
// admission state in the local daemon's QueueState shape: one capacity row per
// concurrency key, its active holder, and a queued waiter behind it, so
// `sparkwing queue --profile` renders it with the one queue renderer.
func TestController_QueueStateViewUnifiesKeys(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "deploy-prod", HolderID: "holder", RunID: "run-a", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	if _, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "deploy-prod", HolderID: "waiter", RunID: "run-b", NodeID: "n2",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); err != nil {
		t.Fatalf("acquire waiter: %v", err)
	}

	resp := mustGet(t, base+"/api/v1/queue/state")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue state status=%d want 200", resp.StatusCode)
	}
	var qs wingwire.QueueState
	if err := json.NewDecoder(resp.Body).Decode(&qs); err != nil {
		t.Fatalf("decode queue state: %v", err)
	}

	if len(qs.Resources) != 1 || qs.Resources[0].Key != "deploy-prod" {
		t.Fatalf("resources = %+v, want one deploy-prod row", qs.Resources)
	}
	if len(qs.Holders) != 1 || qs.Holders[0].RunID != "run-a/n1" {
		t.Fatalf("holders = %+v, want run-a/n1 holding", qs.Holders)
	}
	if qs.Holders[0].Origin != wingwire.OriginController {
		t.Fatalf("holder origin = %q, want controller", qs.Holders[0].Origin)
	}
	if len(qs.Waiters) != 1 || qs.Waiters[0].RunID != "run-b/n2" {
		t.Fatalf("waiters = %+v, want run-b/n2 queued", qs.Waiters)
	}
	if qs.Waiters[0].Position != 1 || len(qs.Waiters[0].WaitingOn) != 1 || qs.Waiters[0].WaitingOn[0] != "deploy-prod" {
		t.Fatalf("waiter = %+v, want position 1 waiting on deploy-prod", qs.Waiters[0])
	}
}
