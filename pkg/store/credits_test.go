package store_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

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
	events, err := teststore.ForTeam(t, s, store.DefaultTeam).ListEventsAfter(ctx, "run-once", 0, 10)
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
