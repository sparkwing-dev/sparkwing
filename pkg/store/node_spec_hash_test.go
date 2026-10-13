package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The spec_hash a run host records on a node is what the node's claim reads
// back, so a remote host checks its own plan against the accepted one.
func TestCreateNode_KeepsTheAcceptedSpecHashForTheClaim(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	const hash = "sha256:9426d2b1671061a6b509ce60d3d811ec20e983a666acdc3fefc057eec4c3cf32"
	if err := s.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "r", NodeID: "build", Status: "pending", SpecHash: hash}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(ctx, "r", "build")
	if err != nil || got.SpecHash != hash {
		t.Fatalf("node = %+v, %v; want spec_hash %s", got, err, hash)
	}
	claimed, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "r", "build", "k8s-job:x", time.Minute, store.NamedClaimOptions{})
	if err != nil || claimed.SpecHash != hash {
		t.Fatalf("claimed node = %+v, %v; want spec_hash %s", claimed, err, hash)
	}
}
