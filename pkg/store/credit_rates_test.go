package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: the rates GitHub charges a Linux x64 runner per second, which is the
// ladder a cluster is provisioned with.
func githubRateTable() store.CreditRateTable {
	return store.CreditRateTable{
		{Cores: 2, MicroPerSecond: 10_000},
		{Cores: 4, MicroPerSecond: 20_000},
		{Cores: 8, MicroPerSecond: 36_667},
		{Cores: 16, MicroPerSecond: 70_000},
		{Cores: 32, MicroPerSecond: 136_667},
		{Cores: 64, MicroPerSecond: 270_000},
	}
}

// safety: the ledger prices a node by the cpu the plan pinned for it, so a
// test that needs a class has to seed the run's plan snapshot.
func readyNodeWithCores(t *testing.T, s *store.Store, runID, nodeID string, cores float64) {
	t.Helper()
	ctx := context.Background()
	plan := []byte(fmt.Sprintf(`{"nodes":[{"id":%q,"modifiers":{"res_cores":%g}}]}`, nodeID, cores))
	if err := s.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := s.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
}

func fundLedger(t *testing.T, s *store.Store, credits int64) {
	t.Helper()
	if _, err := s.GrantCredits(context.Background(), store.CreditGrantPaid,
		credits*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func TestRateTableRoundsACPURequestUpToItsClass(t *testing.T) {
	table := githubRateTable()
	for _, tc := range []struct {
		cores float64
		want  int64
	}{
		{cores: 0, want: 2},
		{cores: 0.25, want: 2},
		{cores: 1, want: 2},
		{cores: 2, want: 2},
		{cores: 2.1, want: 4},
		{cores: 4, want: 4},
		{cores: 5, want: 8},
		{cores: 8, want: 8},
		{cores: 33, want: 64},
		{cores: 64, want: 64},
	} {
		class, err := table.ClassFor(tc.cores)
		if err != nil {
			t.Fatalf("ClassFor(%g): %v", tc.cores, err)
		}
		if class.Cores != tc.want {
			t.Errorf("ClassFor(%g) = %d cores, want %d", tc.cores, class.Cores, tc.want)
		}
	}
}

func TestRateTableRefusesARequestAboveItsLargestClass(t *testing.T) {
	_, err := githubRateTable().ClassFor(96)
	if !errors.Is(err, store.ErrUnpricedCPUClass) {
		t.Fatalf("ClassFor(96) = %v, want ErrUnpricedCPUClass", err)
	}
	var unpriced *store.UnpricedCPUClassError
	if !errors.As(err, &unpriced) {
		t.Fatalf("error %v does not name the request", err)
	}
	if unpriced.Cores != 96 || unpriced.MaxCores != 64 {
		t.Fatalf("refusal = %+v, want 96 cores against a 64-core ceiling", unpriced)
	}
}

func TestRateTableRejectsAnUnusableEntry(t *testing.T) {
	for name, table := range map[string]store.CreditRateTable{
		"empty":          {},
		"zero cores":     {{Cores: 0, MicroPerSecond: 1}},
		"zero rate":      {{Cores: 2, MicroPerSecond: 0}},
		"class twice":    {{Cores: 2, MicroPerSecond: 1}, {Cores: 2, MicroPerSecond: 2}},
		"negative cores": {{Cores: -2, MicroPerSecond: 1}},
	} {
		if err := table.Validate(); err == nil {
			t.Errorf("Validate accepted the %s table", name)
		}
	}
}

// An installation that never set a table bills the GitHub ladder, and its
// four-core class is the single rate setting under another name.
func TestUnsetRateTablePricesTheDefaultLadder(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	table, err := s.CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table: %v", err)
	}
	want := githubRateTable()
	if len(table) != len(want) {
		t.Fatalf("the default table prices %d classes, want %d", len(table), len(want))
	}
	for i, entry := range table {
		if entry != want[i] {
			t.Fatalf("the default table prices %+v, want %+v", entry, want[i])
		}
	}

	single := int64(50_000)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{RateMicroPerSecond: &single}); err != nil {
		t.Fatalf("set the single rate: %v", err)
	}
	table, err = s.CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table after the single rate moved: %v", err)
	}
	if got := table.RateFor(store.CreditRateBaseClassCores); got != 50_000 {
		t.Fatalf("four-core class = %d, want the single rate 50000", got)
	}
	if got := table.RateFor(8); got != 36_667 {
		t.Fatalf("eight-core class = %d, want the ladder's 36667", got)
	}
	set, err := s.CreditRateTableSet(ctx)
	if err != nil || set {
		t.Fatalf("CreditRateTableSet = %v, %v; want false with no table written", set, err)
	}
}

// The single rate setting is the four-core price, so a binary that reads only
// that setting bills a four-core node at the table's own figure.
func TestSetRateTableWritesTheFourCorePriceToTheSingleRate(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		t.Fatalf("read the single rate: %v", err)
	}
	if rate != 20_000 {
		t.Fatalf("single rate = %d, want the four-core price 20000", rate)
	}

	single := int64(21_000)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{RateMicroPerSecond: &single}); err != nil {
		t.Fatalf("set the single rate: %v", err)
	}
	table, err := s.CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table: %v", err)
	}
	if got := table.RateFor(4); got != 21_000 {
		t.Fatalf("four-core class = %d, want the single rate 21000", got)
	}
	if got := table.RateFor(8); got != 36_667 {
		t.Fatalf("eight-core class = %d, want the table's 36667", got)
	}
}

func TestMeteredClaimReservesAtTheNodesCPUClass(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-small", "build", 2)
	readyNodeWithCores(t, s, "run-large", "build", 15.5)

	for _, tc := range []struct {
		runID string
		class int64
		rate  int64
	}{
		{runID: "run-small", class: 2, rate: 10_000},
		{runID: "run-large", class: 16, rate: 70_000},
	} {
		if _, err := s.ClaimNamedNode(ctx, claimant, tc.runID, "build", "pod-"+tc.runID, time.Minute); err != nil {
			t.Fatalf("claim %s: %v", tc.runID, err)
		}
		charge := lastChargeFor(t, s, tc.runID)
		if charge.Kind != store.CreditChargeReservation {
			t.Fatalf("%s wrote %s, want a reservation", tc.runID, charge.Kind)
		}
		if charge.CPUClassCores != tc.class || charge.RateMicroPerSecond != tc.rate {
			t.Fatalf("%s reserved at class %d rate %d, want class %d rate %d",
				tc.runID, charge.CPUClassCores, charge.RateMicroPerSecond, tc.class, tc.rate)
		}
		if want := tc.rate * store.CreditClaimFloorSeconds; charge.AmountMicro != want {
			t.Fatalf("%s reserved %d, want %d", tc.runID, charge.AmountMicro, want)
		}
	}
}

func TestHeartbeatChargesAtTheNodesCPUClass(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-small", "build", 2)
	readyNodeWithCores(t, s, "run-large", "build", 8)

	for _, tc := range []struct {
		runID string
		class int64
		rate  int64
	}{
		{runID: "run-small", class: 2, rate: 10_000},
		{runID: "run-large", class: 8, rate: 36_667},
	} {
		if _, err := s.ClaimNamedNode(ctx, claimant, tc.runID, "build", "pod-"+tc.runID, time.Minute); err != nil {
			t.Fatalf("claim %s: %v", tc.runID, err)
		}
		start := time.Now()
		rewindChargeWindow(t, s, tc.runID, "build", start)
		res, err := s.ChargeNodeCredits(ctx, tc.runID, "build", claimant.TokenPrefix, start.Add(10*time.Second))
		if err != nil {
			t.Fatalf("charge %s: %v", tc.runID, err)
		}
		if res.Charge == nil || res.Charge.Seconds != 10 {
			t.Fatalf("%s charged %+v, want ten seconds", tc.runID, res.Charge)
		}
		if res.Charge.CPUClassCores != tc.class || res.Charge.RateMicroPerSecond != tc.rate {
			t.Fatalf("%s billed class %d at %d, want class %d at %d",
				tc.runID, res.Charge.CPUClassCores, res.Charge.RateMicroPerSecond, tc.class, tc.rate)
		}
		if want := 10 * tc.rate; res.Charge.AmountMicro != want {
			t.Fatalf("%s billed %d, want %d", tc.runID, res.Charge.AmountMicro, want)
		}
	}
}

func TestClaimIsRefusedAboveTheLargestPricedClass(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-huge", "build", 96)

	_, err := s.ClaimNamedNode(ctx, claimant, "run-huge", "build", "pod-1", time.Minute)
	if !errors.Is(err, store.ErrUnpricedCPUClass) {
		t.Fatalf("claim of a 96-core node = %v, want ErrUnpricedCPUClass", err)
	}
	node, err := s.GetNode(ctx, "run-huge", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Claimed {
		t.Fatal("a refused claim left the node claimed")
	}
	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 0 {
		t.Fatalf("a refused claim wrote %+v", charges)
	}
}

// A table change prices the seconds after it and leaves every charge already
// written at the class and rate it was billed at.
func TestARateTableChangeLeavesWrittenChargesAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-repriced", "build", 8)
	if _, err := s.ClaimNamedNode(ctx, claimant, "run-repriced", "build", "pod-1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	start := time.Now()
	rewindChargeWindow(t, s, "run-repriced", "build", start)
	first, err := s.ChargeNodeCredits(ctx, "run-repriced", "build", claimant.TokenPrefix, start.Add(10*time.Second))
	if err != nil || first.Charge == nil {
		t.Fatalf("first charge: %+v %v", first.Charge, err)
	}

	doubled := githubRateTable()
	for i := range doubled {
		doubled[i].MicroPerSecond *= 2
	}
	if err := s.SetCreditRateTable(ctx, doubled); err != nil {
		t.Fatalf("reprice: %v", err)
	}
	second, err := s.ChargeNodeCredits(ctx, "run-repriced", "build", claimant.TokenPrefix, start.Add(20*time.Second))
	if err != nil || second.Charge == nil {
		t.Fatalf("second charge: %+v %v", second.Charge, err)
	}
	if second.Charge.RateMicroPerSecond != 2*36_667 {
		t.Fatalf("the charge after the change billed %d, want %d",
			second.Charge.RateMicroPerSecond, 2*36_667)
	}

	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	for _, c := range charges {
		if c.ID != first.Charge.ID {
			continue
		}
		if c.RateMicroPerSecond != 36_667 || c.AmountMicro != first.Charge.AmountMicro {
			t.Fatalf("the earlier charge now reads %+v, want the rate and amount it was billed at", c)
		}
		return
	}
	t.Fatal("the earlier charge is missing from the history")
}

func lastChargeFor(t *testing.T, s *store.Store, runID string) store.CreditCharge {
	t.Helper()
	charges, err := s.ListCreditCharges(context.Background(), store.CreditHistoryMaxLimit)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	for _, c := range charges {
		if c.RunID == runID {
			return c
		}
	}
	t.Fatalf("no charge for %s", runID)
	return store.CreditCharge{}
}

// A reservation is returned at the price it was taken at, so a table raised
// mid-run cannot refund more than the claim took out.
func TestAnEarlyFinishRefundsAtTheRateTheClaimReserved(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-early", "build", 8)
	if _, err := s.ClaimNamedNode(ctx, claimant, "run-early", "build", "pod-1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	doubled := githubRateTable()
	for i := range doubled {
		doubled[i].MicroPerSecond *= 2
	}
	if err := s.SetCreditRateTable(ctx, doubled); err != nil {
		t.Fatalf("reprice: %v", err)
	}
	res, err := s.FinalizeNodeCredits(ctx, "run-early", "build", claimant.TokenPrefix, time.Now())
	if err != nil || res.Charge == nil {
		t.Fatalf("finalize: %+v %v", res.Charge, err)
	}
	if res.Charge.Kind != store.CreditChargeRefund {
		t.Fatalf("finalize wrote %s, want a refund", res.Charge.Kind)
	}
	if res.Charge.RateMicroPerSecond != 36_667 {
		t.Fatalf("refund priced at %d, want the reserved 36667", res.Charge.RateMicroPerSecond)
	}
	if want := res.Charge.Seconds * 36_667; res.Charge.AmountMicro != want {
		t.Fatalf("refund = %d, want %d", res.Charge.AmountMicro, want)
	}
	if res.BalanceMicro > 10_000*store.MicroCreditsPerCredit {
		t.Fatalf("the refund lifted the balance above what was granted: %d", res.BalanceMicro)
	}
}

// A rate large enough to overflow the reservation is refused before it can
// write a negative charge and hand the payer credits it never bought.
func TestRateTableRefusesARateThatWouldOverflowAReservation(t *testing.T) {
	table := store.CreditRateTable{{Cores: 2, MicroPerSecond: 200_000_000_000_000_000}}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate accepted a rate of 2e17 micro-credits a second")
	}
	if err := (store.CreditRateTable{{Cores: 2, MicroPerSecond: store.MaxCreditRateMicro}}).Validate(); err != nil {
		t.Fatalf("Validate refused the ceiling itself: %v", err)
	}
	s := storetest.Open(t)
	past := int64(store.MaxCreditRateMicro) + 1
	if _, err := s.SetCreditSettings(context.Background(),
		store.CreditSettingsUpdate{RateMicroPerSecond: &past}); err == nil {
		t.Fatal("the single rate accepted a value above the ceiling")
	}
}

// Nothing a claimant says about itself reaches the bill: a claim that asserts a
// tiny runner still pays for the node its plan asked for.
func TestAClaimantsOwnCPUFigureDoesNotLowerTheBill(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-big", "build", 64)

	if _, err := s.ClaimNamedNode(ctx, claimant, "run-big", "build", "pod-1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	charge := lastChargeFor(t, s, "run-big")
	if charge.CPUClassCores != 64 || charge.RateMicroPerSecond != 270_000 {
		t.Fatalf("a 64-core node billed class %d at %d, want the 64-core class",
			charge.CPUClassCores, charge.RateMicroPerSecond)
	}
}

// The operator's ceiling is the only thing that lowers a class, so a cluster
// that gives every node one core bills every node at the smallest class.
func TestTheBillingCPUCeilingHoldsEveryClassDown(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	ceiling := int64(1)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{BillingCPUCeilingCores: &ceiling}); err != nil {
		t.Fatalf("set the ceiling: %v", err)
	}
	readyNodeWithCores(t, s, "run-capped", "build", 64)
	readyNodeWithCores(t, s, "run-huge", "build", 96)

	for _, runID := range []string{"run-capped", "run-huge"} {
		if _, err := s.ClaimNamedNode(ctx, claimant, runID, "build", "pod-"+runID, time.Minute); err != nil {
			t.Fatalf("claim %s: %v", runID, err)
		}
		if charge := lastChargeFor(t, s, runID); charge.CPUClassCores != 2 {
			t.Fatalf("%s billed class %d under a one-core ceiling, want 2", runID, charge.CPUClassCores)
		}
	}

	lifted := int64(0)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{BillingCPUCeilingCores: &lifted}); err != nil {
		t.Fatalf("lift the ceiling: %v", err)
	}
	readyNodeWithCores(t, s, "run-uncapped", "build", 16)
	if _, err := s.ClaimNamedNode(ctx, claimant, "run-uncapped", "build", "pod-2", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if charge := lastChargeFor(t, s, "run-uncapped"); charge.CPUClassCores != 16 {
		t.Fatalf("with no ceiling a 16-core node billed class %d", charge.CPUClassCores)
	}
	negative := int64(-1)
	if _, err := s.SetCreditSettings(ctx,
		store.CreditSettingsUpdate{BillingCPUCeilingCores: &negative}); err == nil {
		t.Fatal("a negative ceiling was accepted")
	}
}

// An empty balance is the refusal a runner already understands, so it is
// reported even for a node the table cannot price.
func TestAnEmptyBalanceIsReportedBeforeTheUnpricedClass(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-broke-huge", "build", 96)

	_, err := s.ClaimNamedNode(ctx, claimant, "run-broke-huge", "build", "pod-1", time.Minute)
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("claim on an empty ledger = %v, want ErrInsufficientCredits", err)
	}
}

// A node nobody can price fails with the reason, because every poller would
// otherwise retry it forever.
func TestFailNodeForUnpricedClassEndsTheNodeWithAnEvent(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	fundLedger(t, s, 10_000)
	if err := s.SetCreditRateTable(ctx, githubRateTable()); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	readyNodeWithCores(t, s, "run-unpriced", "build", 96)

	_, err := s.ClaimNamedNode(ctx, claimant, "run-unpriced", "build", "pod-1", time.Minute)
	var unpriced *store.UnpricedCPUClassError
	if !errors.As(err, &unpriced) {
		t.Fatalf("claim = %v, want an unpriced-class refusal", err)
	}
	if unpriced.RunID != "run-unpriced" || unpriced.NodeID != "build" {
		t.Fatalf("the refusal names %s/%s", unpriced.RunID, unpriced.NodeID)
	}

	if err := s.FailNodeForUnpricedClass(ctx, unpriced, time.Now()); err != nil {
		t.Fatalf("fail the node: %v", err)
	}
	node, err := s.GetNode(ctx, "run-unpriced", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Outcome != "failed" || node.FailureReason != store.FailureUnpricedCPUClass {
		t.Fatalf("node = %s/%s, want a failure naming the unpriced class", node.Outcome, node.FailureReason)
	}
	if !strings.Contains(node.Error, "96") || !strings.Contains(node.Error, "64") {
		t.Fatalf("the node's error %q names neither size", node.Error)
	}
	events, err := s.ListEventsAfter(ctx, "run-unpriced", 0, 50)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, e := range events {
		if e.Kind == store.EventKindCreditsUnpriced {
			return
		}
	}
	t.Fatalf("no %s event on the run: %+v", store.EventKindCreditsUnpriced, events)
}

func TestAStoredRateTableNothingCanReadIsAnError(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('credit_rate_table', 'not json', 1)`,
	); err != nil {
		t.Fatalf("write an unreadable table: %v", err)
	}
	if _, err := s.CreditRateTable(ctx); err == nil {
		t.Fatal("an unreadable stored table read as a usable one")
	}
	if _, err := s.CreditState(ctx, time.Hour); err == nil {
		t.Fatal("the credit state hid an unreadable rate table")
	}
}

// The scalar is the four-core price, so a table that prices no four-core class
// leaves it where it stands.
func TestATableWithoutAFourCoreClassLeavesTheScalarAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if err := s.SetCreditRateTable(ctx, store.CreditRateTable{
		{Cores: 2, MicroPerSecond: 10_000}, {Cores: 8, MicroPerSecond: 36_667},
	}); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		t.Fatalf("read the single rate: %v", err)
	}
	if rate != store.DefaultCreditRateMicro {
		t.Fatalf("single rate = %d, want the default it was left at", rate)
	}
	set, err := s.CreditRateTableSet(ctx)
	if err != nil || !set {
		t.Fatalf("CreditRateTableSet = %v, %v; want true", set, err)
	}
}
