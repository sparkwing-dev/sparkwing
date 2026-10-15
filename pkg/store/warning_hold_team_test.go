package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestWarningHoldRetainsLegacyReleaseAndUnrelatedDispute(t *testing.T) {
	for _, mode := range []string{"legacy-active", "legacy-released", "unrelated-dispute"} {
		t.Run(mode, func(t *testing.T) {
			s := storetest.Open(t)
			ctx, now := context.Background(), time.Now()
			teams := map[store.Team]*store.Tenant{}
			for _, name := range []store.Team{"first", "second"} {
				teams[name] = teamHandle(t, s, name)
				trustWithCard(t, teams[name], now)
				spend(t, s, string(name), "charge_"+string(name), 10000)
			}
			work, _, err := s.DueCardCharges(ctx, now)
			if err != nil || len(work) != 2 {
				t.Fatalf("work=%v error=%v", work, err)
			}
			if _, err := s.HoldTeamForDispute(ctx, "first", "issfr_legacy", "pi_first", "existing hold", now); err != nil {
				t.Fatal(err)
			}
			if mode != "unrelated-dispute" {
				if _, err := s.DB().Exec(`UPDATE credit_freezes SET cause='early_fraud_warning' WHERE dispute_id='issfr_legacy'`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "legacy-released" {
				if _, err := s.ReleaseCreditFreezes(ctx, "first", "issfr_legacy", "", "", now); err != nil {
					t.Fatal(err)
				}
			}
			warning := store.PaymentWarning{WarningID: "issfr_legacy", PaymentIntent: "pi_first", Fingerprint: "fp_1", Actionable: true}
			if _, err := s.RecordPaymentWarning(ctx, warning, now); err != nil {
				t.Fatal(err)
			}
			for repeat := 0; repeat < 2; repeat++ {
				for _, w := range work {
					if paid, _, err := s.SettleCardPayment(ctx, store.CardPayment{Team: w.Team, ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_" + string(w.Team), AmountCents: w.AmountCents, Fingerprint: "fp_1"}, now); err != nil || paid {
						t.Fatalf("team=%s paid=%v error=%v", w.Team, paid, err)
					}
				}
			}
			first, err := teams["first"].CreditFreeze(ctx)
			if err != nil {
				t.Fatal(err)
			}
			second, err := teams["second"].CreditFreeze(ctx)
			if err != nil || !second.Frozen || len(second.Disputes) != 1 {
				t.Fatalf("second=%+v error=%v", second, err)
			}
			switch mode {
			case "legacy-active":
				if !first.Frozen || len(first.Disputes) != 1 || first.Disputes[0] != "issfr_legacy" {
					t.Fatalf("legacy hold rewritten: %+v", first)
				}
			case "legacy-released":
				if first.Frozen || len(first.Disputes) != 0 {
					t.Fatalf("released legacy hold restored: %+v", first)
				}
			case "unrelated-dispute":
				if len(first.Disputes) != 2 {
					t.Fatalf("unrelated dispute suppressed warning hold: %+v", first)
				}
				for _, id := range first.Disputes {
					if id != "issfr_legacy" {
						if n, err := s.ReleaseCreditFreezes(ctx, "first", id, "", "", now); err != nil || n != 1 {
							t.Fatalf("release=%d error=%v", n, err)
						}
					}
				}
				remaining, err := teams["first"].CreditFreeze(ctx)
				if err != nil || len(remaining.Disputes) != 1 || remaining.Disputes[0] != "issfr_legacy" {
					t.Fatalf("unrelated hold released: %+v error=%v", remaining, err)
				}
			}
		})
	}
}
