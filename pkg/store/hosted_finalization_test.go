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
		Scopes:   []string{"nodes.claim", "runs.state", "secrets.read", "logs.write"},
		TTL:      time.Hour,
		Lifetime: time.Hour,
	}
}

func tenantNode(t *testing.T, st *store.Store, tenant, runID, nodeID string, ready bool) {
	t.Helper()
	ctx := store.WithCreatingPrincipal(context.Background(), tenant)
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if ready {
		if err := st.MarkNodeReady(context.Background(), runID, nodeID); err != nil {
			t.Fatal(err)
		}
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
	if tok.ExecutionBinding == nil || tok.ExecutionBinding.RunID != "hosted-run" ||
		tok.ExecutionBinding.RootNodeID != "build" ||
		tok.ExecutionBinding.DelegatedPrincipal != claimant.Principal ||
		tok.ExecutionBinding.DelegatedTokenPrefix != claimant.TokenPrefix ||
		tok.ExecutionBinding.HolderID != n.ClaimedBy ||
		tok.ExecutionBinding.ClaimGeneration != n.ClaimGeneration {
		t.Fatalf("token binding = %+v", tok.ExecutionBinding)
	}
}

func TestHostedExecutionOwnsOnlyRecordedDynamicChildrenAndCountsOneRunner(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tenantNode(t, st, "tenant-a", "dynamic-run", "build", false)
	if err := st.CreateNode(ctx, store.Node{RunID: "dynamic-run", NodeID: "build/static", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	claimant := meteredClaimant(t, st, "cloud-pool")
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "dynamic-payment", "admin"); err != nil {
		t.Fatal(err)
	}
	result, err := st.FinalizeExecutorClaimRound(ctx, "dynamic-run", "build", store.DispatchHosted,
		hostedSpec("dynamic-run", "build", claimant))
	if err != nil {
		t.Fatal(err)
	}
	binding := *result.Hosted.Token.ExecutionBinding
	fence := store.NodeClaimFence{
		Claimant: claimant, HolderID: binding.HolderID, ClaimGeneration: binding.ClaimGeneration,
	}
	execution := store.WithExecutionCredentialFence(ctx, store.ExecutionCredentialFence{Binding: binding, Fence: fence})
	if err := st.CreateNode(execution, store.Node{RunID: "dynamic-run", NodeID: "build/dynamic", Status: "pending"}); err != nil {
		t.Fatalf("create dynamic child: %v", err)
	}
	if owned, err := st.ExecutionCredentialOwnsNode(ctx, binding, "dynamic-run", "build/dynamic", time.Now()); err != nil || !owned {
		t.Fatalf("recorded dynamic child ownership = %v, %v", owned, err)
	}
	if owned, err := st.ExecutionCredentialOwnsNode(ctx, binding, "dynamic-run", "build/static", time.Now()); err != nil || owned {
		t.Fatalf("static prefix node ownership = %v, %v; want false", owned, err)
	}
	usage, err := st.ComputeUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Runners != 1 || usage.ByPrincipal["tenant-a"] != 1 {
		t.Fatalf("dynamic child counted as another runner: %+v", usage)
	}
}

func TestHostedFinalizationRefusalsLeaveNoCredentialOrClaim(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ttl      time.Duration
		lifetime time.Duration
	}{
		{name: "zero ttl", ttl: 0, lifetime: time.Hour},
		{name: "negative ttl", ttl: -time.Second, lifetime: time.Hour},
		{name: "zero lifetime", ttl: time.Minute, lifetime: 0},
		{name: "over lifetime", ttl: time.Hour + time.Second, lifetime: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			seedClaimedNode(t, st, "lifetime-run", "build")
			claimant := meteredClaimant(t, st, "lifetime-pool")
			spec := hostedSpec("lifetime-run", "build", claimant)
			spec.TTL, spec.Lifetime = tc.ttl, tc.lifetime
			before, _ := st.ListTokens("", true)
			if _, err := st.FinalizeExecutorClaimRound(context.Background(), "lifetime-run", "build", store.DispatchHosted, spec); err == nil {
				t.Fatal("invalid hosted credential lifetime was accepted")
			}
			after, _ := st.ListTokens("", true)
			if len(after) != len(before) {
				t.Fatalf("lifetime refusal left %d token(s)", len(after)-len(before))
			}
			n, _ := st.GetNode(context.Background(), "lifetime-run", "build")
			if n.Claimed {
				t.Fatalf("lifetime refusal claimed node: %+v", n)
			}
		})
	}

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

func TestMeteredRunnerLimitUsesRunTenantAcrossPoolsAndClaimPaths(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	poolA := meteredClaimant(t, st, "pool-a")
	poolB := meteredClaimant(t, st, "pool-b")
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "shared-payment", "admin"); err != nil {
		t.Fatalf("grant credits: %v", err)
	}
	if err := st.SetComputeLimit(ctx, store.ComputeLimitConcurrentRunners, 1); err != nil {
		t.Fatalf("set runner limit: %v", err)
	}
	tenantNode(t, st, "tenant-a", "warm-run", "build", true)
	if _, err := st.ClaimNextReadyNode(ctx, poolA, "warm-holder", time.Minute, nil); err != nil {
		t.Fatalf("claim warm node: %v", err)
	}
	tenantNode(t, st, "tenant-a", "hosted-run", "build", false)
	_, err := st.FinalizeExecutorClaimRound(ctx, "hosted-run", "build", store.DispatchHosted,
		hostedSpec("hosted-run", "build", poolB))
	if !errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("same tenant across warm and hosted pools = %v, want compute limit", err)
	}
	n, _ := st.GetNode(ctx, "hosted-run", "build")
	if n.Claimed {
		t.Fatalf("runner-limit refusal claimed hosted node: %+v", n)
	}
	var claimPrincipal, quotaPrincipal string
	if err := st.DB().QueryRow(`SELECT claim_principal, claim_quota_principal FROM nodes WHERE run_id = 'warm-run' AND node_id = 'build'`).Scan(
		&claimPrincipal, &quotaPrincipal); err != nil {
		t.Fatal(err)
	}
	if claimPrincipal != poolA.Principal || quotaPrincipal != "tenant-a" {
		t.Fatalf("warm identities = claim %q quota %q", claimPrincipal, quotaPrincipal)
	}
}

func TestMeteredRunnerLimitSeparatesTenantsSharingOnePool(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	pool := meteredClaimant(t, st, "shared-pool")
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "tenant-payment", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetComputeLimit(ctx, store.ComputeLimitConcurrentRunners, 1); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ tenant, run string }{{"tenant-a", "run-a"}, {"tenant-b", "run-b"}} {
		tenantNode(t, st, item.tenant, item.run, "build", false)
		if _, err := st.FinalizeExecutorClaimRound(ctx, item.run, "build", store.DispatchHosted,
			hostedSpec(item.run, "build", pool)); err != nil {
			t.Fatalf("claim %s: %v", item.tenant, err)
		}
	}
}

func TestMeteredRunnerLimitJoinsOneTenantAcrossTwoHostedPools(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	poolA := meteredClaimant(t, st, "hosted-pool-a")
	poolB := meteredClaimant(t, st, "hosted-pool-b")
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "two-pool-payment", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetComputeLimit(ctx, store.ComputeLimitConcurrentRunners, 1); err != nil {
		t.Fatal(err)
	}
	tenantNode(t, st, "tenant-a", "run-a", "build", false)
	if _, err := st.FinalizeExecutorClaimRound(ctx, "run-a", "build", store.DispatchHosted,
		hostedSpec("run-a", "build", poolA)); err != nil {
		t.Fatal(err)
	}
	tenantNode(t, st, "tenant-a", "run-b", "build", false)
	if _, err := st.FinalizeExecutorClaimRound(ctx, "run-b", "build", store.DispatchHosted,
		hostedSpec("run-b", "build", poolB)); !errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("second pool claim = %v, want tenant limit", err)
	}
}
