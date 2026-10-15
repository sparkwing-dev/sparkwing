package store_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// safety: a node whose plan pins no cpu resolves to one core, which the
// smallest class of the default rate table covers, so that is the price these
// fixtures are billed at.
var unpinnedNodeRateMicro = store.DefaultCreditRateTable(store.DefaultCreditRateMicro).RateFor(1)

func seedClaimedNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
}

// safety: the ledger prices work against the claim identity, so a test that
// needs charging must claim under a token the operator marked metered.
func meteredClaimant(t *testing.T, s *store.Store, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := s.CreateTokenWith(context.Background(), principal, store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("mint a metered token: %v", err)
	}
	return store.ClaimIdentity{Principal: principal, TokenPrefix: tok.Prefix}
}

func readyNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	seedClaimedNode(t, s, runID, nodeID)
	if err := s.MarkNodeReady(context.Background(), runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
}

// safety: values are inlined because the two dialects spell bound parameters
// differently and every one here is a test-owned integer or identifier.
func rewindChargeWindow(t *testing.T, s *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	res, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE nodes SET credit_charged_through = %d,
		 execution_started_at = COALESCE(execution_started_at, %d)
		 WHERE run_id = '%s' AND node_id = '%s'`,
		at.UnixNano(), at.UnixNano(), runID, nodeID))
	if err != nil {
		t.Fatalf("rewind the charge window: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil || changed != 1 {
		t.Fatalf("rewind changed %d rows (%v)", changed, err)
	}
}

func acknowledgeClaimedExecution(
	t *testing.T, s *store.Store, claimant store.ClaimIdentity, n *store.Node,
) time.Time {
	t.Helper()
	if err := s.AcknowledgeNodeExecutionStart(context.Background(), n.RunID, n.NodeID, claimant,
		store.ExecutionStart{
			HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
			ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
			AttemptOrdinal: n.AttemptsConsumed + 1,
		}); err != nil {
		t.Fatalf("acknowledge execution start: %v", err)
	}
	started, err := s.GetNode(context.Background(), n.RunID, n.NodeID)
	if err != nil {
		t.Fatalf("read execution start: %v", err)
	}
	if started.ExecutionStartedAt == nil {
		t.Fatal("execution start was not recorded")
	}
	return *started.ExecutionStartedAt
}

func expireNodeLease(t *testing.T, s *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE nodes SET lease_expires_at = %d WHERE run_id = '%s' AND node_id = '%s'`,
		at.UnixNano(), runID, nodeID)); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
}

func chargeWindowAnchor(t *testing.T, s *store.Store, runID, nodeID string) int64 {
	t.Helper()
	var anchor int64
	if err := s.DB().QueryRow(fmt.Sprintf(
		`SELECT credit_charged_through FROM nodes WHERE run_id = '%s' AND node_id = '%s'`,
		runID, nodeID)).Scan(&anchor); err != nil {
		t.Fatalf("read the charge window: %v", err)
	}
	return anchor
}

func setChargeWindowWithoutExecution(t *testing.T, s *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	res, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE nodes SET credit_charged_through = %d, execution_started_at = NULL
		 WHERE run_id = '%s' AND node_id = '%s'`, at.UnixNano(), runID, nodeID))
	if err != nil {
		t.Fatalf("set the pre-execution charge window: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil || changed != 1 {
		t.Fatalf("set pre-execution charge window changed %d rows (%v)", changed, err)
	}
}

func TestCreditsGrantRejectsUnknownKindAndZero(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := tenant(t, s, store.DefaultTeam).GrantCredits(ctx, "gift", 10, "", "admin"); err == nil {
		t.Fatal("expected an unknown grant kind to be refused")
	}
	if _, err := tenant(t, s, store.DefaultTeam).GrantCredits(ctx, store.CreditGrantFree, 0, "", "admin"); err == nil {
		t.Fatal("expected a zero grant to be refused")
	}
}

func TestFinalizeNodeCreditsRefundsAReservationWhenExecutionNeverStarts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-never-started", "build")
	granted := int64(100 * store.MicroCreditsPerCent)
	if _, err := tenant(t, s, store.DefaultTeam).GrantCredits(ctx, store.CreditGrantPaid, granted, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// safety: a dispatcher's claim whose pod never renewed it is a machine that
	// never started, which is the claim a finish refunds whole.
	if _, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: claimant.TokenPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run-never-started", NodeID: "build"}, Available: match.Resources{Cores: 128}}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	res, err := s.FinalizeNodeCredits(ctx, "run-never-started", "build", claimant.TokenPrefix,
		time.Now().Add(15*time.Minute))
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if res.Charge == nil || res.Charge.Kind != store.CreditChargeRefund ||
		res.Charge.Seconds != -int64(store.MinBillableSeconds) {
		t.Fatalf("finalize charge = %+v, want the complete reservation refunded", res.Charge)
	}
	balance, err := tenant(t, s, store.DefaultTeam).CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != granted {
		t.Fatalf("balance = %d, want the full grant %d restored", balance, granted)
	}
	totals, err := s.CreditLedgerTotals(ctx)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.SettledSeconds != 0 || totals.RefundedMicro != totals.ReservedMicro {
		t.Fatalf("settled totals = %+v, want no execution charge and a full refund", totals)
	}
	res, err = s.FinalizeNodeCredits(ctx, "run-never-started", "build", claimant.TokenPrefix,
		time.Now().Add(15*time.Minute))
	if err != nil || res.Charge != nil {
		t.Fatalf("duplicate finalize = %+v, %v; want no ledger movement", res, err)
	}
}

func TestRunnerChargesKeepTheRunPrincipalAfterRunDeletion(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	pool := meteredClaimant(t, s, "runner:shared-pool")
	if _, err := tenant(t, s, store.DefaultTeam).GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCent, "pay_attribution", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	create := func(principal, runID string) {
		t.Helper()
		created := store.WithCreatingPrincipal(ctx, principal)
		if err := s.CreateRun(created, store.Run{
			ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
		if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatalf("create node %s: %v", runID, err)
		}
	}
	create("tenant-a", "run-a")
	create("tenant-b", "run-b")

	claimedAt := time.Now()
	if _, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: pool.TokenPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run-a", NodeID: "build"}, Available: match.Resources{Cores: 128}}); err != nil {
		t.Fatalf("claim tenant A: %v", err)
	}
	if res, err := s.FinalizeNodeCredits(ctx, "run-a", "build", pool.TokenPrefix,
		claimedAt.Add(5*time.Second)); err != nil || res.Charge == nil || res.Charge.Kind != store.CreditChargeRefund {
		t.Fatalf("finalize tenant A = %+v, %v; want a refund", res.Charge, err)
	}
	if res, err := s.FinalizeNodeCredits(ctx, "run-a", "build", pool.TokenPrefix,
		claimedAt.Add(5*time.Second)); err != nil || res.Charge != nil {
		t.Fatalf("repeat finalize tenant A = %+v, %v; want no charge", res.Charge, err)
	}

	if _, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: pool.TokenPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run-b", NodeID: "build"}, Available: match.Resources{Cores: 128}}); err != nil {
		t.Fatalf("claim tenant B: %v", err)
	}
	usageStart := time.Now()
	rewindChargeWindow(t, s, "run-b", "build", usageStart)
	usageAt := usageStart.Add(10 * time.Second)
	if res, err := store.ChargeNodeCreditsForTest(ctx, s, "run-b", "build", pool.TokenPrefix, usageAt); err != nil ||
		res.Charge == nil || res.Charge.Kind != store.CreditChargeUsage {
		t.Fatalf("charge tenant B = %+v, %v; want usage", res.Charge, err)
	}
	if res, err := store.ChargeNodeCreditsForTest(ctx, s, "run-b", "build", pool.TokenPrefix, usageAt); err != nil ||
		res.Charge != nil {
		t.Fatalf("repeat charge tenant B = %+v, %v; want no charge", res.Charge, err)
	}

	for _, runID := range []string{"run-a", "run-b"} {
		if err := teststore.ForTeam(t, s, store.DefaultTeam).DeleteRun(ctx, runID); err != nil {
			t.Fatalf("delete %s: %v", runID, err)
		}
	}
	charges, err := tenant(t, s, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 4 {
		t.Fatalf("charges = %+v, want two reservations, one refund and one usage charge", charges)
	}
	for _, charge := range charges {
		want := "tenant-a"
		if charge.RunID == "run-b" {
			want = "tenant-b"
		}
		if charge.Principal != want {
			t.Errorf("%s/%s principal = %q, want %q", charge.RunID, charge.Kind, charge.Principal, want)
		}
		if charge.Principal == pool.Principal {
			t.Errorf("%s/%s attributed to runner pool %q", charge.RunID, charge.Kind, pool.Principal)
		}
	}
}

func TestTokenMeteredMarkerIsOperatorSet(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	_, plain, err := s.CreateToken("agent:local", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatalf("create plain token: %v", err)
	}
	if plain.Metered {
		t.Fatal("a plain mint must not be metered")
	}
	metered, err := s.TokenMetered(ctx, plain.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if metered {
		t.Fatal("TokenMetered reported a plain token as metered")
	}

	_, cloud, err := s.CreateTokenWith(ctx, "agent:cloud", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now(),
		store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("create metered token: %v", err)
	}
	if !cloud.Metered {
		t.Fatal("a metered mint must carry the marker")
	}
	metered, err = s.TokenMetered(ctx, cloud.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if !metered {
		t.Fatal("TokenMetered lost the marker")
	}

	if err := s.SetTokenMetered(ctx, plain.Prefix, true); err != nil {
		t.Fatalf("set metered: %v", err)
	}
	metered, err = s.TokenMetered(ctx, plain.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if !metered {
		t.Fatal("set-metered did not take")
	}
	if err := s.SetTokenMetered(ctx, plain.Prefix, false); err != nil {
		t.Fatalf("unset metered: %v", err)
	}
	metered, err = s.TokenMetered(ctx, plain.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if metered {
		t.Fatal("unset-metered did not take")
	}

	if err := s.SetTokenMetered(ctx, "swu_nosuchtok", true); err == nil {
		t.Fatal("expected an unknown prefix to be refused")
	}
	metered, err = s.TokenMetered(ctx, "swu_nosuchtok")
	if err != nil {
		t.Fatalf("unknown prefix: %v", err)
	}
	if metered {
		t.Fatal("an unknown prefix must not read as metered")
	}
}

func TestAppendEventOnceCollapsesRepeats(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-once", "build")

	wrote, err := tenant(t, s, store.DefaultTeam).AppendEventOnce(ctx, "run-once", "build", store.EventKindCreditsBlocked, []byte(`{"balance_micro":0}`))
	if err != nil {
		t.Fatalf("append once: %v", err)
	}
	if !wrote {
		t.Fatal("the first append must write")
	}
	wrote, err = tenant(t, s, store.DefaultTeam).AppendEventOnce(ctx, "run-once", "build", store.EventKindCreditsBlocked, []byte(`{"balance_micro":0}`))
	if err != nil {
		t.Fatalf("append once again: %v", err)
	}
	if wrote {
		t.Fatal("the second append must collapse into the first")
	}
	events, err := s.ListEventsAfter(ctx, "run-once", 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seen := 0
	for _, e := range events {
		if e.Kind == store.EventKindCreditsBlocked {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("credits_blocked events = %d, want 1", seen)
	}
}

func TestFormatCreditsRendersTwoPlaces(t *testing.T) {
	cases := []struct {
		micro int64
		want  string
	}{
		{0, "0.00"},
		{store.MicroCreditsPerCredit, "1.00"},
		{1000 * store.MicroCreditsPerCredit, "1000.00"},
		{store.MicroCreditsPerCredit / 2, "0.50"},
		{-3 * store.MicroCreditsPerCredit / 2, "-1.50"},
	}
	for _, c := range cases {
		if got := store.FormatCredits(c.micro); got != c.want {
			t.Errorf("FormatCredits(%d) = %q, want %q", c.micro, got, c.want)
		}
	}
}

func TestCreditSettingsBoundTheRateSoTheLedgerCannotOverflow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	highest := int64(store.MaxCreditRateMicro)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateMicroPerSecond: &highest}); err != nil {
		t.Fatalf("the highest allowed rate was refused: %v", err)
	}
	if floor := highest * store.MinBillableSeconds; floor <= 0 {
		t.Fatalf("minimum reservation = %d at the highest allowed rate; it must stay positive", floor)
	}

	for name, rate := range map[string]int64{
		"one past the ceiling": store.MaxCreditRateMicro + 1,
		"the int64 maximum":    math.MaxInt64,
		"zero":                 0,
		"negative":             -1,
	} {
		if _, err := s.AsOperator().SetCreditSettings(ctx,
			store.CreditSettingsUpdate{RateMicroPerSecond: &rate}); !errors.Is(err, store.ErrInvalidCreditSetting) {
			t.Errorf("%s was accepted as a rate: %v", name, err)
		}
	}
	settings, err := s.AsOperator().CreditSettings(ctx)
	if err != nil {
		t.Fatalf("read rate: %v", err)
	}
	if settings.RateMicroPerSecond != store.MaxCreditRateMicro {
		t.Fatalf("rate = %d; a refused write moved it", settings.RateMicroPerSecond)
	}
}

func TestCreditSettingsBoundTheChargeCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	lowest := store.MinCreditMaxChargeSeconds
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{MaxChargeSeconds: &lowest}); err != nil {
		t.Fatalf("the lowest allowed cap was refused: %v", err)
	}
	highest := int64(store.MaxCreditMaxChargeSeconds)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{MaxChargeSeconds: &highest}); err != nil {
		t.Fatalf("the highest allowed cap was refused: %v", err)
	}
	for name, cap := range map[string]int64{
		"one under the floor":  store.MinCreditMaxChargeSeconds - 1,
		"one past the ceiling": store.MaxCreditMaxChargeSeconds + 1,
		"zero":                 0,
		"the int64 maximum":    math.MaxInt64,
	} {
		if _, err := s.AsOperator().SetCreditSettings(ctx,
			store.CreditSettingsUpdate{MaxChargeSeconds: &cap}); !errors.Is(err, store.ErrInvalidCreditSetting) {
			t.Errorf("%s was accepted as a charge cap: %v", name, err)
		}
	}
}

func TestSetCreditSettingsWritesEveryNamedValueOrNone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	rate, grace, maxCharge := int64(30_000), int64(0), int64(45)
	got, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{
		RateMicroPerSecond: &rate, GraceSeconds: &grace, MaxChargeSeconds: &maxCharge,
	})
	if err != nil {
		t.Fatalf("set all three: %v", err)
	}
	if got.RateMicroPerSecond != rate || got.GraceSeconds != grace || got.MaxChargeSeconds != maxCharge {
		t.Fatalf("settings = %+v", got)
	}

	bad := int64(-5)
	keep := int64(20)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{
		MaxChargeSeconds: &keep, RateMicroPerSecond: &bad,
	}); !errors.Is(err, store.ErrInvalidCreditSetting) {
		t.Fatalf("a mixed update = %v, want a refusal", err)
	}
	got, err = s.AsOperator().CreditSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if got.MaxChargeSeconds != maxCharge {
		t.Fatalf("charge cap = %d; the good half of a refused update was written", got.MaxChargeSeconds)
	}

	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{}); !errors.Is(
		err, store.ErrInvalidCreditSetting) {
		t.Fatalf("an update naming nothing = %v, want a refusal", err)
	}
}

// safety: the claim consumes the whole balance, so every later heartbeat reads
// a spent ledger and only the grace period decides when the node stops.
func exhaustedReservedNode(t *testing.T, s *store.Store, runID string, grace int64) (
	store.ClaimIdentity, time.Time,
) {
	t.Helper()
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:"+runID)
	readyNode(t, s, runID, "build")
	floor := unpinnedNodeRateMicro * store.MinBillableSeconds
	if _, err := tenant(t, s, store.DefaultTeam).GrantCredits(ctx, store.CreditGrantFree, floor, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	// safety: a dispatched claim bills from its execution start, which is the
	// instant these tests measure the reservation and the grace period from.
	_, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: claimant.TokenPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runID, NodeID: "build"}, Available: match.Resources{Cores: 128}})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	n, err := s.GetNode(ctx, runID, "build")
	if err != nil {
		t.Fatal(err)
	}
	start := acknowledgeClaimedExecution(t, s, claimant, n)
	return claimant, start
}

func chargeAt(
	t *testing.T, s *store.Store, runID string, claimant store.ClaimIdentity, at time.Time,
) store.CreditChargeResult {
	t.Helper()
	res, err := store.ChargeNodeCreditsForTest(context.Background(), s, runID, "build", claimant.TokenPrefix, at)
	if err != nil {
		t.Fatalf("charge at %s: %v", at, err)
	}
	return res
}

func TestChargeNodeCreditsCountsGraceFromTheReservationEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	reserved := time.Duration(store.MinBillableSeconds) * time.Second
	for _, tc := range []struct {
		name              string
		grace             int64
		survives, cancels time.Duration
	}{
		{
			"grace zero stops at the first heartbeat past the reservation",
			0, reserved, reserved + 5*time.Second,
		},
		{
			"grace sixty stops a minute past the reservation",
			60, reserved + 55*time.Second, reserved + 65*time.Second,
		},
		{
			"grace one twenty stops two minutes past the reservation",
			120, reserved + 115*time.Second, reserved + 125*time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			runID := fmt.Sprintf("run-grace-%d", tc.grace)
			claimant, start := exhaustedReservedNode(t, s, runID, tc.grace)

			res := chargeAt(t, s, runID, claimant, start.Add(tc.survives))
			if res.BalanceMicro > 0 {
				t.Fatalf("balance at %s = %d, want it spent", tc.survives, res.BalanceMicro)
			}
			if res.Cancel {
				t.Fatalf("cancelled at %s, inside the %ds grace period past the reservation",
					tc.survives, tc.grace)
			}

			rewindChargeWindow(t, s, runID, "build", start.Add(tc.survives))
			if res := chargeAt(t, s, runID, claimant, start.Add(tc.cancels)); !res.Cancel {
				t.Fatalf("still running at %s, past the reservation and a %ds grace period",
					tc.cancels, tc.grace)
			}
		})
	}
}

func TestChargeNodeCreditsNeverCancelsInsideTheClaimReservation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-reserved", "build")
	floor := unpinnedNodeRateMicro * store.MinBillableSeconds
	if _, err := tenant(t, s, store.DefaultTeam).GrantCredits(ctx, store.CreditGrantFree, floor, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	grace := int64(0)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	_, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: claimant.TokenPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run-reserved", NodeID: "build"}, Available: match.Resources{Cores: 128}})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	n, err := s.GetNode(ctx, "run-reserved", "build")
	if err != nil {
		t.Fatal(err)
	}
	start := acknowledgeClaimedExecution(t, s, claimant, n)

	// safety: the claim consumed the whole balance, so every heartbeat inside
	// the reservation reads a spent ledger and must still let it run.
	for _, at := range []time.Duration{3 * time.Second, 10 * time.Second, store.MinBillableSeconds * time.Second} {
		res, err := store.ChargeNodeCreditsForTest(ctx, s, "run-reserved", "build", claimant.TokenPrefix, start.Add(at))
		if err != nil {
			t.Fatalf("charge at %s: %v", at, err)
		}
		if res.BalanceMicro > 0 {
			t.Fatalf("balance at %s = %d, want it spent", at, res.BalanceMicro)
		}
		if res.Cancel {
			t.Fatalf("cancelled at %s, inside the minimum the claim reserved and paid for", at)
		}
	}

	res, err := store.ChargeNodeCreditsForTest(ctx, s, "run-reserved", "build", claimant.TokenPrefix,
		start.Add((store.MinBillableSeconds+store.PoolHeartbeatInterval/time.Second)*time.Second))
	if err != nil {
		t.Fatalf("charge past the reservation: %v", err)
	}
	if !res.Cancel {
		t.Fatalf("grace zero must cancel at the first heartbeat past the reservation, got %+v", res)
	}
}
