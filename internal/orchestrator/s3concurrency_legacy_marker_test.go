package orchestrator_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// legacyInheritedMarker is the node id every release from v0.15.0 to
// v0.40.0 wrote for a holder that joined a parent's slot.
const legacyInheritedMarker = "\x00inherited:parent/-"

// seedLegacySlotDoc writes the slot document a pre-upgrade binary left in
// the bucket. Both the object key and the document shape are spelled out
// here rather than reused from production so the test still fails if the
// reader stops accepting what that binary actually wrote.
func seedLegacySlotDoc(t *testing.T, art storage.ArtifactStore, key string, parentCost int) {
	t.Helper()
	lease := time.Now().Add(time.Hour).UnixNano()
	claimed := time.Now().Add(-time.Minute).UnixNano()
	doc := map[string]any{
		"key":      key,
		"capacity": 10,
		"holders": []map[string]any{
			{
				"holder_id":         "parent/-",
				"run_id":            "parent",
				"node_id":           "n1",
				"claimed_at_ns":     claimed,
				"lease_expires_ns":  lease,
				"cost":              parentCost,
				"declared_capacity": 10,
			},
			{
				"holder_id":         "child/-",
				"run_id":            "child",
				"node_id":           legacyInheritedMarker,
				"claimed_at_ns":     claimed,
				"lease_expires_ns":  lease,
				"cost":              0,
				"declared_capacity": -1,
			},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal legacy slot doc: %v", err)
	}
	sum := sha256.Sum256([]byte(key))
	objKey := "concurrency/" + hex.EncodeToString(sum[:]) + ".json"
	if err := art.Put(context.Background(), objKey, bytes.NewReader(body)); err != nil {
		t.Fatalf("seed legacy slot doc: %v", err)
	}
}

func TestS3Concurrency_LegacyInheritedMarkerHidesNodeID(t *testing.T) {
	art, _ := openIntegrationS3(t)
	key := "g:legacy-node-id"
	seedLegacySlotDoc(t, art, key, 8)
	c := orchestrator.NewS3Concurrency(art)

	child, err := c.ObserveSlot(context.Background(), key, "child/-")
	if err != nil {
		t.Fatalf("ObserveSlot(child): %v", err)
	}
	if child.NodeID != "" {
		t.Errorf("child node id = %q, want empty for an inherited holder", child.NodeID)
	}
	if child.DeclaredCapacity != 0 {
		t.Errorf("child declared capacity = %d, want 0", child.DeclaredCapacity)
	}
}

func TestS3Concurrency_LegacyInheritedHolderTakesReleasedParentCost(t *testing.T) {
	art, _ := openIntegrationS3(t)
	key := "g:legacy-handoff"
	seedLegacySlotDoc(t, art, key, 8)
	c := orchestrator.NewS3Concurrency(art)

	if err := c.ReleaseSlot(context.Background(), key, "parent/-", "success", "", "", 0); err != nil {
		t.Fatalf("ReleaseSlot(parent): %v", err)
	}
	child, err := c.ObserveSlot(context.Background(), key, "child/-")
	if err != nil {
		t.Fatalf("ObserveSlot(child): %v", err)
	}
	if child.Cost != 8 {
		t.Fatalf("child cost after parent release = %d, want the parent's 8", child.Cost)
	}

	follower := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "follower/-", RunID: "follower",
		Capacity: 10, Cost: 8, Policy: store.OnLimitQueue, Lease: time.Minute,
	})
	if follower.Kind != store.AcquireQueued {
		t.Fatalf("follower = %s, want Queued because the child still accounts for cost 8", follower.Kind)
	}
}

func TestS3Concurrency_CancelOthersSupersedesLegacyInheritedHolder(t *testing.T) {
	art, _ := openIntegrationS3(t)
	key := "g:legacy-cancel-others"
	seedLegacySlotDoc(t, art, key, 8)
	c := orchestrator.NewS3Concurrency(art)

	evictor := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "evictor/-", RunID: "evictor",
		Capacity: 10, Cost: 8, Policy: store.OnLimitCancelOthers, Lease: time.Minute,
	})
	if evictor.Kind != store.AcquireCancellingOthers {
		t.Fatalf("evictor = %s, want CancellingOthers", evictor.Kind)
	}
	if !containsString(evictor.SupersededIDs, "child/-") {
		t.Fatalf("superseded ids = %v, want the inherited child", evictor.SupersededIDs)
	}
	_, superseded, err := c.HeartbeatSlot(context.Background(), key, "child/-", time.Minute)
	if err != nil {
		t.Fatalf("HeartbeatSlot(child): %v", err)
	}
	if !superseded {
		t.Fatal("child heartbeat did not report superseded")
	}
}
