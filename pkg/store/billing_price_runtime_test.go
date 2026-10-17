package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestMeteredAwardPricesPinnedResourcesProperty(t *testing.T) {
	for _, pin := range []struct {
		cores               float64
		memory, class, rate int64
	}{
		{1, 0, 2, 10_000}, {1.5, 0, 2, 10_000}, {3, 0, 4, 20_000}, {8, 0, 8, 36_667}, {15.5, 0, 16, 70_000}, {64, 0, 64, 270_000}, {3, 20 << 30, 8, 36_667},
	} {
		t.Run(fmt.Sprintf("cores=%g/memory=%d", pin.cores, pin.memory), func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, true)
			s.billing = nil
			if _, _, err := (&Tenant{s: s, team: "a"}).SetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "operator", Reason: "price coverage"}, *now); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE credit_grants SET amount_micro=100000000")
			rates := CreditRateTable{{Cores: 2, MicroPerSecond: 10_000}, {Cores: 4, MicroPerSecond: 20_000}, {Cores: 8, MicroPerSecond: 36_667}, {Cores: 16, MicroPerSecond: 70_000}, {Cores: 64, MicroPerSecond: 270_000}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET execution_json=?", []byte(fmt.Sprintf(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":%g},"memory_bytes":{"set":true,"value":%d}}}}`, pin.cores, pin.memory)))
			sealedRun, err := (&Tenant{s: s, team: "a"}).GetRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if err := (&Tenant{s: s, team: "a"}).UpsertProfilePin(t.Context(), sealedRun.Pipeline, "job", 96, 1<<40); err != nil {
				t.Fatal(err)
			}
			demand.Available, demand.Capacity = match.Resources{Cores: 128, MemoryBytes: 1 << 40}, match.Resources{Cores: 128, MemoryBytes: 1 << 40}
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
			if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
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
				if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
					t.Fatal(err)
				}
			}
			unitBillingTotal(t, s, 70*pin.rate, false)
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

func TestMeteredAwardRefusesUnpricedResourcesAtomically(t *testing.T) {
	for _, cores := range []int{9, 16, 96} {
		t.Run(fmt.Sprint(cores), func(t *testing.T) {
			s, demand, _ := meteredUnitFixture(t, true)
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET execution_json=?", []byte(fmt.Sprintf(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":%d}}}}`, cores)))
			demand.Available, demand.Capacity = match.Resources{Cores: 128}, match.Resources{Cores: 128}
			_, err := s.ClaimUnit(t.Context(), demand)
			if !errors.Is(err, ErrUnpricedCPUClass) {
				t.Fatalf("unpriced claim: %v", err)
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
		})
	}
}

func TestARefundDebitsTheCardThatPaid(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, true)
	rewriteSchemaExec(t, s.db, "UPDATE teams SET card_fingerprint='original' WHERE name='a'")
	claim, _ := meteredUnitClaim(t, s, demand)
	rewriteSchemaExec(t, s.db, "UPDATE teams SET card_fingerprint='replacement' WHERE name='a'")
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	charges, err := (&Tenant{s: s, team: claim.Team}).ListCreditCharges(t.Context(), 100)
	if err != nil || len(charges) != 2 {
		t.Fatalf("charges: %+v, %v", charges, err)
	}
	for _, fingerprint := range []string{"original", "replacement"} {
		var amount int64
		if err := s.db.QueryRow("SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges WHERE card_fingerprint=?", fingerprint).Scan(&amount); err != nil || amount != 0 {
			t.Fatalf("%s spend=%d, %v", fingerprint, amount, err)
		}
	}
	unitBillingTotal(t, s, 0, true)
}
