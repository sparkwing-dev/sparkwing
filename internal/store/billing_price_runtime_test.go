package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestMeteredAwardPricesRequiredSizeDespiteProfilePins(t *testing.T) {
	for _, pin := range []struct {
		class, rate int64
	}{
		{2, 10_000}, {4, 20_000}, {8, 36_667}, {16, 70_000}, {64, 270_000},
	} {
		t.Run(fmt.Sprint(pin.class), func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, true)
			if _, _, err := (&Tenant{s: s, team: "a"}).TestOnlySetBillingTrust(t.Context(), BillingTrustChange{Trust: billingTrustGranted, Actor: "operator", Reason: "price coverage"}, *now); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE credit_grants SET amount_micro=100000000")
			rates := CreditRateTable{{Cores: 2, MicroPerSecond: 10_000}, {Cores: 4, MicroPerSecond: 20_000}, {Cores: 8, MicroPerSecond: 36_667}, {Cores: 16, MicroPerSecond: 70_000}, {Cores: 64, MicroPerSecond: 270_000}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET execution_json=?", []byte(fmt.Sprintf(`{"requires":{"set":true,"value":["sparkwing-%dcpu"]}}`, pin.class)))
			sealedRun, err := (&Tenant{s: s, team: "a"}).GetRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if err := (&Tenant{s: s, team: "a"}).UpsertProfilePin(t.Context(), sealedRun.Pipeline, "job", 96, 1<<40); err != nil {
				t.Fatal(err)
			}
			claim, _ := meteredUnitClaim(t, s, demand)
			tenant := &Tenant{s: s, team: claim.Team}
			charges, err := tenant.ListCreditCharges(t.Context(), 100)
			if err != nil || len(charges) != 1 || charges[0].CPUClassCores != pin.class || charges[0].RateMicroPerSecond != pin.rate || charges[0].AmountMicro != 60*pin.rate {
				t.Fatalf("reservation: %+v, %v", charges, err)
			}
			node, err := tenant.GetNode(t.Context(), claim.RunID, claim.NodeID)
			if err != nil || node.CreditCPUClassCores != pin.class {
				t.Fatalf("price projection: %+v, %v", node, err)
			}
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
				t.Fatal(err)
			}
			for i := range rates {
				rates[i].MicroPerSecond *= 2
			}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
				t.Fatal(err)
			}
			for range 7 {
				*now = now.Add(10 * time.Second)
				if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
					t.Fatal(err)
				}
			}
			unitBillingTotal(t, s, 130*pin.rate, false)
			if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			unitBillingTotal(t, s, 70*pin.rate, true)
			rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
			unitBillingTotal(t, s, 70*pin.rate, true)
			charges, err = tenant.ListCreditCharges(t.Context(), 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, charge := range charges {
				if charge.CPUClassCores != pin.class || charge.RateMicroPerSecond != pin.rate {
					t.Fatalf("award repriced: %+v", charge)
				}
			}
		})
	}
}

func TestMeteredAwardRefusesUnpricedSizesAtomically(t *testing.T) {
	for _, cores := range []int{8, 16, 64} {
		t.Run(fmt.Sprint(cores), func(t *testing.T) {
			s, demand, _ := meteredUnitFixture(t, true)
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET execution_json=?", []byte(fmt.Sprintf(`{"requires":{"set":true,"value":["sparkwing-%dcpu"]}}`, cores)))
			_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if !errors.Is(err, ErrUnpricedCPUClass) {
				t.Fatalf("unpriced claim: %v", err)
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
		})
	}
}
