package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func hostedSpec(runID, nodeID string, claimant store.ClaimIdentity) *store.HostedClaimSpec {
	return &store.HostedClaimSpec{
		Binding: store.ExecutionCredentialBinding{
			RunID: runID, RootNodeID: nodeID,
			DelegatedPrincipal: claimant.Principal, DelegatedTokenPrefix: claimant.TokenPrefix,
		},
		Scopes: []string{"nodes.claim", "runs.state", "secrets.read", "logs.write"},
		TTL:    time.Hour,
	}
}

func TestHostedFinalizationAwardsBoundCredentialAtomically(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, st, "hosted-run", "build")
	claimant := meteredClaimant(t, st, "cloud-pool")
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "hosted-payment", "admin"); err != nil {
		t.Fatalf("grant credits: %v", err)
	}

	result, err := st.FinalizeExecutorClaimRound(ctx, "hosted-run", "build", store.DispatchHosted,
		hostedSpec("hosted-run", "build", claimant))
	if err != nil {
		t.Fatalf("finalize hosted: %v", err)
	}
	if result.Hosted == nil || result.Hosted.Node == nil || result.Hosted.RawBearer == "" || result.Hosted.Token == nil {
		t.Fatalf("hosted result = %+v", result)
	}
	if result.Revoked || result.Pending {
		t.Fatalf("hosted result reports fallback state: %+v", result)
	}
	n := result.Hosted.Node
	if !n.Claimed || n.ExecutorKind != "k8s" || n.ExecutorLocation != "cloud" || n.CreditCPUClassCores == 0 {
		t.Fatalf("hosted node = %+v", n)
	}
	if n.ReadyAt != nil {
		t.Fatalf("direct hosted node was exposed as ready at %v", n.ReadyAt)
	}
	tok, err := st.LookupToken(result.Hosted.RawBearer, time.Now())
	if err != nil {
		t.Fatalf("authenticate hosted bearer: %v", err)
	}
	if tok.ExecutionBinding == nil || *tok.ExecutionBinding != hostedSpec("hosted-run", "build", claimant).Binding {
		t.Fatalf("token binding = %+v", tok.ExecutionBinding)
	}
}

func TestHostedFinalizationRefusalsLeaveNoCredentialOrClaim(t *testing.T) {
	t.Run("dispatcher absent", func(t *testing.T) {
		st := storetest.Open(t)
		seedClaimedNode(t, st, "absent-run", "build")
		before, err := st.ListTokens("", true)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.FinalizeExecutorClaimRound(context.Background(), "absent-run", "build", store.DispatchHosted, nil)
		if !errors.Is(err, store.ErrHostedExecutionUnavailable) {
			t.Fatalf("error = %v", err)
		}
		after, err := st.ListTokens("", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Fatalf("tokens changed from %d to %d", len(before), len(after))
		}
		n, err := st.GetNode(context.Background(), "absent-run", "build")
		if err != nil || n.Claimed {
			t.Fatalf("node = %+v, err %v", n, err)
		}
	})

	t.Run("credit refusal", func(t *testing.T) {
		st := storetest.Open(t)
		seedClaimedNode(t, st, "poor-run", "build")
		claimant := meteredClaimant(t, st, "poor-pool")
		before, _ := st.ListTokens("", true)
		_, err := st.FinalizeExecutorClaimRound(context.Background(), "poor-run", "build", store.DispatchHosted,
			hostedSpec("poor-run", "build", claimant))
		if !errors.Is(err, store.ErrInsufficientCredits) {
			t.Fatalf("error = %v", err)
		}
		after, _ := st.ListTokens("", true)
		if len(after) != len(before) {
			t.Fatalf("credit refusal left %d token(s)", len(after)-len(before))
		}
		n, _ := st.GetNode(context.Background(), "poor-run", "build")
		if n.Claimed {
			t.Fatalf("credit refusal claimed node: %+v", n)
		}
	})
}

func TestHostedAndWarmClaimsSharePoolRunnerLimit(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, st, "shared-pool")
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "shared-payment", "admin"); err != nil {
		t.Fatalf("grant credits: %v", err)
	}
	if err := st.SetComputeLimit(ctx, store.ComputeLimitConcurrentRunners, 1); err != nil {
		t.Fatalf("set runner limit: %v", err)
	}
	readyNode(t, st, "warm-run", "build")
	if _, err := st.ClaimNextReadyNode(ctx, claimant, "warm-holder", time.Minute, nil); err != nil {
		t.Fatalf("claim warm node: %v", err)
	}
	seedClaimedNode(t, st, "hosted-run", "build")
	_, err := st.FinalizeExecutorClaimRound(ctx, "hosted-run", "build", store.DispatchHosted,
		hostedSpec("hosted-run", "build", claimant))
	if !errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("mixed warm and hosted claim error = %v, want compute limit", err)
	}
	n, _ := st.GetNode(ctx, "hosted-run", "build")
	if n.Claimed {
		t.Fatalf("runner-limit refusal claimed hosted node: %+v", n)
	}
}
