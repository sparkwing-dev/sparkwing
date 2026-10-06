package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSharedFraudWarningHoldsEveryTeam(t *testing.T) {
	for _, order := range []string{"warning-first", "payment-first", "concurrent-payments"} {
		t.Run(order, func(t *testing.T) {
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
				t.Fatalf("charges=%v error=%v", work, err)
			}
			payments := map[store.Team]store.CardPayment{}
			for _, w := range work {
				payments[w.Team] = store.CardPayment{Team: w.Team, ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_" + string(w.Team), AmountCents: w.AmountCents, Fingerprint: "fp_1"}
			}
			if order == "payment-first" {
				if paid, _, err := s.SettleCardPayment(ctx, payments["first"], now); err != nil || !paid {
					t.Fatalf("first payment=%v error=%v", paid, err)
				}
			}
			warning := store.PaymentWarning{WarningID: "issfr_shared", PaymentIntent: "pi_first", Fingerprint: "fp_1", Actionable: true}
			if _, err := s.RecordPaymentWarning(ctx, warning, now); err != nil {
				t.Fatal(err)
			}
			settle := func(team store.Team) error {
				if order == "payment-first" && team == "first" {
					return nil
				}
				paid, _, err := s.SettleCardPayment(ctx, payments[team], now)
				if err != nil {
					return fmt.Errorf("team %s: %w", team, err)
				}
				if paid {
					return fmt.Errorf("team %s: warned payment was granted", team)
				}
				return nil
			}
			if order == "concurrent-payments" {
				var wg sync.WaitGroup
				results := make(chan error, 2)
				for team := range teams {
					wg.Go(func() { results <- settle(team) })
				}
				wg.Wait()
				close(results)
				for err := range results {
					if err != nil {
						t.Fatal(err)
					}
				}
			} else {
				for _, team := range []store.Team{"first", "second"} {
					if err := settle(team); err != nil {
						t.Fatal(err)
					}
				}
			}
			ids := map[store.Team]string{}
			for team, tenant := range teams {
				frozen, err := tenant.CreditFreeze(ctx)
				if err != nil || !frozen.Frozen || len(frozen.Disputes) != 1 {
					t.Fatalf("%s freeze=%+v error=%v", team, frozen, err)
				}
				ids[team] = frozen.Disputes[0]
				readyTeamNode(t, s, tenant, "run-"+string(team), "build")
				claimant := meteredTeamClaimant(t, tenant, "agent:"+string(team))
				_, claimErr := s.ClaimNextReadyNode(ctx, claimant, "private-pod", time.Minute, nil)
				var refused *store.InsufficientCreditsError
				if !errors.As(claimErr, &refused) || !refused.Frozen {
					t.Fatalf("%s claim=%v, want frozen refusal", team, claimErr)
				}

				owner, found, err := s.DisputeTeam(ctx, ids[team])
				if err != nil || !found || owner != team {
					t.Fatalf("hold owner=%s found=%v error=%v", owner, found, err)
				}
				standing, err := tenant.SpendStanding(ctx, now)
				if err != nil || standing.Billing.Trusted || standing.CreditLimitMicro != 0 {
					t.Fatalf("%s standing=%+v error=%v", team, standing, err)
				}
				want := int64(-10000 * store.MicroCreditsPerCent)
				if order == "payment-first" && team == "first" {
					want = 0
				}
				if balance, err := tenant.CreditBalanceMicro(ctx); err != nil || balance != want {
					t.Fatalf("%s balance=%d want=%d error=%v", team, balance, want, err)
				}
				if err := settle(team); err != nil {
					t.Fatal(err)
				}
			}
			if ids["first"] == ids["second"] {
				t.Fatal("teams share a freeze identity")
			}
			if _, err := s.RecordPaymentWarning(ctx, warning, now); err != nil {
				t.Fatal(err)
			}
			if order == "payment-first" {
				req := store.CreditGrantRequest{Kind: store.CreditGrantReversal, AmountMicro: -10000 * store.MicroCreditsPerCent, Reference: "re_first", Reverses: "pi_first", CreatedBy: "billing"}
				for i := 0; i < 2; i++ {
					if _, err := teams["first"].RecordCreditGrant(ctx, req); err != nil {
						t.Fatal(err)
					}
				}
				if balance, err := teams["first"].CreditBalanceMicro(ctx); err != nil || balance != -10000*store.MicroCreditsPerCent {
					t.Fatalf("refund balance=%d error=%v", balance, err)
				}
			}
			if n, err := s.ReleaseCreditFreezes(ctx, "first", ids["first"], now); err != nil || n != 1 {
				t.Fatalf("release=%d error=%v", n, err)
			}
			if err := settle("first"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordPaymentWarning(ctx, warning, now); err != nil {
				t.Fatal(err)
			}
			a, _ := teams["first"].CreditFreeze(ctx)
			b, _ := teams["second"].CreditFreeze(ctx)
			if a.Frozen || !b.Frozen || len(b.Disputes) != 1 {
				t.Fatalf("release/replay first=%+v second=%+v", a, b)
			}
			refunds, err := s.DueCardRefunds(ctx, now)
			if err != nil || len(refunds) != 0 {
				t.Fatalf("hold created unexpected refund work=%v error=%v", refunds, err)
			}
		})
	}
}

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
				if _, err := s.ReleaseCreditFreezes(ctx, "first", "issfr_legacy", now); err != nil {
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
						if n, err := s.ReleaseCreditFreezes(ctx, "first", id, now); err != nil || n != 1 {
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
