package store_test

import (
	"context"
	"errors"
	"fmt"
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

// An installation that never set a table pays one price for every class, which
// is what the single rate setting charged on its own.
func TestUnsetRateTablePricesEveryClassAtTheSingleRate(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	table, err := s.CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table: %v", err)
	}
	if len(table) == 0 {
		t.Fatal("the default rate table prices no class")
	}
	for _, entry := range table {
		if entry.MicroPerSecond != store.DefaultCreditRateMicro {
			t.Fatalf("the %d-core class costs %d, want the default %d",
				entry.Cores, entry.MicroPerSecond, store.DefaultCreditRateMicro)
		}
	}

	if err := s.SetCreditRateMicroPerSecond(ctx, 50_000); err != nil {
		t.Fatalf("set the single rate: %v", err)
	}
	table, err = s.CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table after the single rate moved: %v", err)
	}
	for _, entry := range table {
		if entry.MicroPerSecond != 50_000 {
			t.Fatalf("the %d-core class costs %d, want 50000", entry.Cores, entry.MicroPerSecond)
		}
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

	if err := s.SetCreditRateMicroPerSecond(ctx, 21_000); err != nil {
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
