package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A runner's claim answers with a token bound to the claim and its claimant,
// and a later claim of the node retires the earlier claim's token.
func TestRunnerClaimToken_BindsTheClaimAndDiesWithItsGeneration(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runner := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runnera"}
	readyNode(t, s, "run-r", "build")
	first, err := s.ClaimNextReadyNode(ctx, runner, "holder-1", time.Minute, nil)
	if err != nil || first == nil || first.ClaimToken == "" {
		t.Fatalf("claim = %+v, %v; want a claim token", first, err)
	}
	rc, err := s.AuthorizeRunnerClaim(ctx, first.ClaimToken, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rc.Claimant != runner || rc.Token.RunID != "run-r" || rc.Token.NodeID != "build" || rc.Token.Kind != store.ClaimTokenNode ||
		rc.Fence.HolderID != "holder-1" || rc.Fence.ClaimGeneration != first.ClaimGeneration {
		t.Fatalf("resolved %+v", rc)
	}
	expireAndReap(t, claimedFixture{s: s, identity: runner, node: first})
	if _, err := s.AuthorizeRunnerClaim(ctx, first.ClaimToken, time.Now()); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("a reaped claim's token = %v, want ErrClaimNotLive", err)
	}
	next, err := s.ClaimNextReadyNode(ctx, runner, "holder-2", time.Minute, nil)
	if err != nil || next == nil || next.ClaimGeneration <= first.ClaimGeneration {
		t.Fatalf("reclaim = %+v, %v", next, err)
	}
	if _, err := s.AuthorizeRunnerClaim(ctx, first.ClaimToken, time.Now()); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("the earlier generation's token = %v, want ErrClaimNotLive", err)
	}
	if _, err := s.AuthorizeRunnerClaim(ctx, next.ClaimToken, time.Now()); err != nil {
		t.Fatalf("the live generation's token = %v", err)
	}
}

// A launcher's token answers only its own claim routes, and an unbound claim
// mints no token at all.
func TestRunnerClaimToken_OnlyARunnersBoundClaimIsDelegated(t *testing.T) {
	f := claimForToken(t, "run-launch")
	if f.node.ClaimToken != "" {
		t.Fatalf("an unbound claim minted %q", f.node.ClaimToken)
	}
	raw := f.mint(t, time.Now())
	if _, err := f.s.AuthorizeRunnerClaim(context.Background(), raw, time.Now()); !errors.Is(err, store.ErrClaimNotRunner) {
		t.Fatalf("a launcher's token = %v, want ErrClaimNotRunner", err)
	}
}
