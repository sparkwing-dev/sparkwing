package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedSealedNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending", RequestedSlots: 1,
	}); err != nil {
		t.Fatalf("CreateNode(%s/%s): %v", runID, nodeID, err)
	}
	if err := st.TestOnlyMarkNodeReadySealed(ctx, runID, nodeID); err != nil {
		t.Fatalf("seal %s/%s: %v", runID, nodeID, err)
	}
}

// An enrolled executor is offered and may offer for its own team's nodes
// alone. The other team's node is the oldest in the queue, so a preparation
// scan without the team predicate binds it first, and an offer naming it
// is refused as not found before the attestation check can say it exists.
func TestAssistedExecutorStaysInItsCredentialsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")

	if err := st.CreateRun(ctx, store.Run{ID: "run-home", Pipeline: "release", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	seedSealedNode(t, st, "run-home", "candidate")
	if err := acme.CreateRun(ctx, store.Run{ID: "run-acme", Pipeline: "release", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	seedSealedNode(t, st, "run-acme", "candidate")

	_, tok, err := acme.CreateToken(ctx, "agent:acme-helper", store.TokenKindRunner,
		[]string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	if err := st.EnrollExecutor(ctx, tok.Prefix, store.Executor{
		Name: "acme-helper", Kind: "agent", Location: "local", Principal: tok.Principal,
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
		Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30},
	}); err != nil {
		t.Fatalf("EnrollExecutor: %v", err)
	}
	reportCtx, err := executionpolicy.WithRuntimeReport(ctx, executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "linux", GOARCH: "amd64",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.HeartbeatExecutor(reportCtx, claimant, "acme-helper",
		store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30}, 0, time.Now()); err != nil {
		t.Fatalf("HeartbeatExecutor: %v", err)
	}

	sink := executionpolicy.NewPreparationSink()
	prepareCtx := executionpolicy.WithPreparationSink(ctx, sink)
	if _, err := st.PrepareNextExecutorClaim(prepareCtx, claimant, "acme-helper"); err != nil &&
		!errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("PrepareNextExecutorClaim: %v", err)
	}
	if binding := sink.Load(); binding.RunID != "run-acme" {
		t.Fatalf("an acme executor was prepared for %s/%s, want its own team's run-acme", binding.RunID, binding.NodeID)
	}

	summary, err := st.SchedulingSummary(ctx, "run-home", "candidate")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.OfferExecutorClaim(ctx, claimant, store.ExecutorClaimOffer{
		ExecutorName: "acme-helper", HolderID: "holder", RunID: "run-home", NodeID: "candidate",
		ReservationID: "reservation", ResourceDigest: summary.ResourceDigest, Slot: 0,
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an acme executor offering for another team's node got %v, want not found", err)
	}
}
