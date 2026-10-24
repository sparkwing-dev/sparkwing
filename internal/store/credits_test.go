package store_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// safety: a node whose plan pins no cpu resolves to one core, which the
// smallest class of the default rate table covers, so that is the price these
// fixtures are billed at.
var unpinnedNodeRateMicro = int64(9_000)

func seedClaimedNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{RunSummary: runstate.RunSummary{ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now()}}); err != nil {
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

	highest := int64(1000000000000)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateMicroPerSecond: &highest}); err != nil {
		t.Fatalf("the highest allowed rate was refused: %v", err)
	}
	if floor := highest * store.SliceSeconds; floor <= 0 {
		t.Fatalf("minimum reservation = %d at the highest allowed rate; it must stay positive", floor)
	}

	for name, rate := range map[string]int64{
		"one past the ceiling": 1000000000000 + 1,
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
	if settings.RateMicroPerSecond != 1000000000000 {
		t.Fatalf("rate = %d; a refused write moved it", settings.RateMicroPerSecond)
	}
}

func TestSetCreditSettingsWritesEveryNamedValueOrNone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	rate, warm := int64(30_000), int64(8)
	got, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{
		RateMicroPerSecond: &rate, WarmCPUClassCores: &warm,
	})
	if err != nil {
		t.Fatalf("set both: %v", err)
	}
	if got.RateMicroPerSecond != rate || got.WarmCPUClassCores != warm {
		t.Fatalf("settings = %+v", got)
	}

	bad := int64(-5)
	keep := int64(16)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{
		WarmCPUClassCores: &keep, RateMicroPerSecond: &bad,
	}); !errors.Is(err, store.ErrInvalidCreditSetting) {
		t.Fatalf("a mixed update = %v, want a refusal", err)
	}
	got, err = s.AsOperator().CreditSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if got.WarmCPUClassCores != warm {
		t.Fatalf("warm class = %d; the good half of a refused update was written", got.WarmCPUClassCores)
	}

	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{}); !errors.Is(
		err, store.ErrInvalidCreditSetting) {
		t.Fatalf("an update naming nothing = %v, want a refusal", err)
	}
}
