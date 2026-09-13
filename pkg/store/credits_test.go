package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

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

func TestCreditsBalanceIsGrantsMinusCharges(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("fresh balance = %d, want 0", balance)
	}

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_123", "admin"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 50*store.MicroCreditsPerCredit, "", "admin"); err != nil {
		t.Fatalf("free grant: %v", err)
	}
	balance, err = s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(1050 * store.MicroCreditsPerCredit); balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}

	grants, err := s.ListCreditGrants(ctx, 10)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %d, want 2", len(grants))
	}
	if grants[0].Kind != store.CreditGrantFree {
		t.Fatalf("newest grant kind = %q, want free", grants[0].Kind)
	}
}

func TestCreditsGrantRejectsUnknownKindAndZero(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.GrantCredits(ctx, "gift", 10, "", "admin"); err == nil {
		t.Fatal("expected an unknown grant kind to be refused")
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 0, "", "admin"); err == nil {
		t.Fatal("expected a zero grant to be refused")
	}
}

func TestChargeNodeCreditsBillsElapsedSecondsIdempotently(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-charge", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	start := time.Now()
	if err := s.StartNodeMetering(ctx, "run-charge", "build", start); err != nil {
		t.Fatalf("start metering: %v", err)
	}

	res, err := s.ChargeNodeCredits(ctx, "run-charge", "build", "swr_abcd1234", start.Add(10*time.Second))
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds != 10 {
		t.Fatalf("first charge = %+v, want 10 seconds", res.Charge)
	}
	if want := int64(10 * store.DefaultCreditRateMicro); res.Charge.AmountMicro != want {
		t.Fatalf("charge amount = %d, want %d", res.Charge.AmountMicro, want)
	}
	if res.Cancel {
		t.Fatal("a funded ledger must not ask for a cancellation")
	}

	res, err = s.ChargeNodeCredits(ctx, "run-charge", "build", "swr_abcd1234", start.Add(10*time.Second))
	if err != nil {
		t.Fatalf("repeat charge: %v", err)
	}
	if res.Charge != nil {
		t.Fatalf("repeat charge wrote %+v, want nothing", res.Charge)
	}

	res, err = s.ChargeNodeCredits(ctx, "run-charge", "build", "swr_abcd1234", start.Add(25*time.Second))
	if err != nil {
		t.Fatalf("third charge: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds != 15 {
		t.Fatalf("third charge = %+v, want 15 seconds", res.Charge)
	}

	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 2 {
		t.Fatalf("charges = %d, want 2", len(charges))
	}
	if charges[0].TokenPrefix != "swr_abcd1234" {
		t.Fatalf("charge token prefix = %q", charges[0].TokenPrefix)
	}

	state, err := s.CreditState(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if want := int64(25 * store.DefaultCreditRateMicro); state.ChargedMicro != want {
		t.Fatalf("charged = %d, want %d", state.ChargedMicro, want)
	}
	if state.BalanceMicro != state.GrantedMicro-state.ChargedMicro {
		t.Fatalf("balance %d does not equal granted %d minus charged %d",
			state.BalanceMicro, state.GrantedMicro, state.ChargedMicro)
	}
	if state.BurnMicro != state.ChargedMicro {
		t.Fatalf("burn = %d, want the whole day's charges %d", state.BurnMicro, state.ChargedMicro)
	}
	if state.RateMicroPerSecond != store.DefaultCreditRateMicro {
		t.Fatalf("rate = %d, want the default %d", state.RateMicroPerSecond, store.DefaultCreditRateMicro)
	}
}

func TestChargeNodeCreditsCancelsAfterGrace(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-empty", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, store.DefaultCreditRateMicro, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := s.SetCreditGraceSeconds(ctx, 30); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	start := time.Now()
	if err := s.StartNodeMetering(ctx, "run-empty", "build", start); err != nil {
		t.Fatalf("start metering: %v", err)
	}
	res, err := s.ChargeNodeCredits(ctx, "run-empty", "build", "swr_metered001", start.Add(5*time.Second))
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if res.BalanceMicro > 0 {
		t.Fatalf("balance = %d, want it spent", res.BalanceMicro)
	}
	if res.Cancel {
		t.Fatal("the grace period had not elapsed yet")
	}

	res, err = s.ChargeNodeCredits(ctx, "run-empty", "build", "swr_metered001", start.Add(40*time.Second))
	if err != nil {
		t.Fatalf("charge after grace: %v", err)
	}
	if !res.Cancel {
		t.Fatalf("expected a cancellation after the grace period, got %+v", res)
	}

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "pay_2", "admin"); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	res, err = s.ChargeNodeCredits(ctx, "run-empty", "build", "swr_metered001", start.Add(45*time.Second))
	if err != nil {
		t.Fatalf("charge after top-up: %v", err)
	}
	if res.Cancel {
		t.Fatal("a top-up must clear the exhaustion stamp")
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

	_, cloud, err := s.CreateTokenWith("agent:cloud", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now(),
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

func TestTokenRotationCarriesTheMeteredMarker(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, cloud, err := s.CreateTokenWith("agent:cloud", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now(),
		store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, newTok, _, err := s.RotateToken(cloud.Prefix, time.Hour, 0, time.Now())
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !newTok.Metered {
		t.Fatal("a rotation dropped the metering marker")
	}
	metered, err := s.TokenMetered(ctx, newTok.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if !metered {
		t.Fatal("the rotated token is not metered in the database")
	}
}

func TestAppendEventOnceCollapsesRepeats(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-once", "build")

	wrote, err := s.AppendEventOnce(ctx, "run-once", "build", store.EventKindCreditsBlocked, []byte(`{"balance_micro":0}`))
	if err != nil {
		t.Fatalf("append once: %v", err)
	}
	if !wrote {
		t.Fatal("the first append must write")
	}
	wrote, err = s.AppendEventOnce(ctx, "run-once", "build", store.EventKindCreditsBlocked, []byte(`{"balance_micro":0}`))
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

func TestOldestWaitingReadyNodeNamesTheWaitingRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runID, nodeID, err := s.OldestWaitingReadyNode(ctx)
	if err != nil {
		t.Fatalf("oldest waiting: %v", err)
	}
	if runID != "" || nodeID != "" {
		t.Fatalf("empty queue named %s/%s", runID, nodeID)
	}
	seedClaimedNode(t, s, "run-wait", "build")
	if err := s.MarkNodeReady(ctx, "run-wait", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	runID, nodeID, err = s.OldestWaitingReadyNode(ctx)
	if err != nil {
		t.Fatalf("oldest waiting: %v", err)
	}
	if runID != "run-wait" || nodeID != "build" {
		t.Fatalf("oldest waiting = %s/%s, want run-wait/build", runID, nodeID)
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
