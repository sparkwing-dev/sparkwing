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
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
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
	if err := teststore.SeedNodeReady(s, context.Background(), runID, nodeID); err != nil {
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
	if _, err := tenant(t, s, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: "gift", AmountMicro: 10, Reference: "", CreatedBy: "admin"}); err == nil {
		t.Fatal("expected an unknown grant kind to be refused")
	}
	if _, err := tenant(t, s, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 0, Reference: "", CreatedBy: "admin"}); err == nil {
		t.Fatal("expected a zero grant to be refused")
	}
}

func TestFinalizeNodeCreditsRefundsAReservationWhenExecutionNeverStarts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-never-started", "build")
	granted := int64(100 * store.MicroCreditsPerCent)
	if _, err := tenant(t, s, store.DefaultTeam).TestOnlySettlePaidCredit(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: granted, Reference: "pay_1", CreatedBy: "admin"}); err != nil {
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
	totals, err := s.AsOperator().CreditLedgerTotals(ctx)
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
	if _, err := tenant(t, s, store.DefaultTeam).TestOnlySettlePaidCredit(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_attribution", CreatedBy: "admin"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	create := func(principal, runID string) {
		t.Helper()
		created := store.WithCreatingPrincipal(ctx, principal)
		if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(created, store.Run{
			ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
		if err := teststore.SeedNode(s, ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
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
		if err := teststore.ForTeam(t, s, store.DefaultTeam).RemoveRunForTest(ctx, runID); err != nil {
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
