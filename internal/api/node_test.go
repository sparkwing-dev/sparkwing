package api

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPublicNodeIsIdempotent(t *testing.T) {
	raw := &store.Node{
		RunID: "run", NodeID: "node", ClaimedBy: "private-holder",
		ClaimWorkerID: "desktop", ExecutorKind: "agent", ClaimGeneration: 9,
	}
	first := PublicNode(raw)
	second := PublicNode(first)
	if !first.Claimed || !second.Claimed || second.ExecutorName != "desktop" {
		t.Fatalf("projected node lost public attribution: first=%+v second=%+v", first, second)
	}
	if second.ClaimedBy != "" || second.ClaimGeneration != 0 {
		t.Fatalf("projected node exposed claim identity: %+v", second)
	}
	if raw.Claimed || raw.ExecutorName != "" || raw.ClaimedBy != "private-holder" {
		t.Fatalf("projection mutated source: %+v", raw)
	}
}
