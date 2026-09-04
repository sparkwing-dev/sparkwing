package orchestrator_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: this is the whole of the v0.40.0 reader, copied so a change to
// the marker this binary writes has to break the test before it breaks
// every runner in a fleet that has not upgraded.
func v0_40_0ReadsAsInherited(nodeID string) bool {
	return strings.HasPrefix(nodeID, "\x00inherited:")
}

var inheritedMarkerForms = map[string]string{
	"released":   "\x00inherited:parent/-",
	"unreleased": `\inherited:parent/-`,
}

// safety: the object key and document shape are spelled out here rather
// than reused from production, or the test would follow the writer
// wherever it moves instead of holding it to the bytes a released runner
// reads.
func slotObjectKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "concurrency/" + hex.EncodeToString(sum[:]) + ".json"
}

func seedSlotDoc(t *testing.T, art storage.ArtifactStore, key, childNodeID string, parentCost int) {
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
				"node_id":           childNodeID,
				"claimed_at_ns":     claimed,
				"lease_expires_ns":  lease,
				"cost":              0,
				"declared_capacity": -1,
			},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal slot doc: %v", err)
	}
	if err := art.Put(context.Background(), slotObjectKey(key), bytes.NewReader(body)); err != nil {
		t.Fatalf("seed slot doc: %v", err)
	}
}

func TestS3Concurrency_InheritedMarkerHidesNodeID(t *testing.T) {
	for form, marker := range inheritedMarkerForms {
		t.Run(form, func(t *testing.T) {
			art, _ := openIntegrationS3(t)
			key := "g:marker-node-id-" + form
			seedSlotDoc(t, art, key, marker, 8)
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
		})
	}
}

func TestS3Concurrency_InheritedHolderTakesReleasedParentCost(t *testing.T) {
	for form, marker := range inheritedMarkerForms {
		t.Run(form, func(t *testing.T) {
			art, _ := openIntegrationS3(t)
			key := "g:marker-handoff-" + form
			seedSlotDoc(t, art, key, marker, 8)
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
		})
	}
}

func TestS3Concurrency_CancelOthersSupersedesMarkedInheritedHolder(t *testing.T) {
	for form, marker := range inheritedMarkerForms {
		t.Run(form, func(t *testing.T) {
			art, _ := openIntegrationS3(t)
			key := "g:marker-cancel-others-" + form
			seedSlotDoc(t, art, key, marker, 8)
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
		})
	}
}

func TestS3Concurrency_InheritedHolderStaysReadableByAReleasedRunner(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:marker-round-trip"
	ctx := context.Background()

	parent := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "parent/-", RunID: "parent",
		Capacity: 10, Cost: 8, Policy: store.OnLimitQueue, Lease: time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent = %s, want Granted", parent.Kind)
	}
	child := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "child/-", InheritedHolderID: "parent/-", RunID: "child",
		Capacity: 10, Cost: 8, Policy: store.OnLimitQueue, Lease: time.Minute,
	})
	if child.Kind != store.AcquireGranted {
		t.Fatalf("child = %s, want Granted", child.Kind)
	}

	rc, err := art.Get(ctx, slotObjectKey(key))
	if err != nil {
		t.Fatalf("read slot object: %v", err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read slot object body: %v", err)
	}
	var doc struct {
		Holders []struct {
			HolderID string `json:"holder_id"`
			NodeID   string `json:"node_id"`
		} `json:"holders"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode slot object: %v", err)
	}
	found := false
	for _, h := range doc.Holders {
		if h.HolderID != "child/-" {
			continue
		}
		found = true
		if !v0_40_0ReadsAsInherited(h.NodeID) {
			t.Fatalf("child node id %q is not inherited to a v0.40.0 runner; it would read the marker as a node id, "+
				"charge the child no cost, and skip it when cancelling others", h.NodeID)
		}
	}
	if !found {
		t.Fatalf("no child holder in the slot object: %s", body)
	}
}
