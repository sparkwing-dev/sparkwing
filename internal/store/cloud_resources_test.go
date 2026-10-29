package store

import (
	"reflect"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestCloudAllocationKeepsCommittedClassAcrossExchangeAndPriceChanges(t *testing.T) {
	property := func(rate uint16, additional uint8) bool {
		s, demand, _ := meteredUnitFixture(t, true)
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{}`))
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil {
			t.Fatal(err)
		}
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}
		before, err := s.AsOperator().CloudAwardResources(t.Context(), ref)
		if err != nil || before == nil || before.Cores != 2 || before.MemoryBytes != CPUClassMemoryBytes(2) {
			t.Fatalf("implicit demand lost priced class: %+v %v", before, err)
		}
		table := CreditRateTable{{Cores: 4, MicroPerSecond: int64(rate) + 1}, {Cores: int64(additional) + 5, MicroPerSecond: int64(rate) + 2}}
		if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
			t.Fatal(err)
		}
		changed, err := s.AsOperator().CloudAwardResources(t.Context(), ref)
		if err != nil || !reflect.DeepEqual(before, changed) {
			t.Fatalf("price change resized a committed award: %+v %v", changed, err)
		}
		exchanged, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: ref.ID})
		if err != nil {
			t.Fatal(err)
		}
		direct, err := s.AsOperator().CloudAwardResources(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: exchanged.Capability.Prefix})
		if err != nil || !reflect.DeepEqual(before, direct) {
			t.Fatalf("exchange resized a committed award: %+v %v", direct, err)
		}
		if _, err := s.AsOperator().CloudAwardResources(t.Context(), ref); err == nil {
			t.Fatal("consumed bootstrap retained allocation lookup")
		}
		return true
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 24})); err != nil {
		t.Fatal(err)
	}
}

func TestCustomerMachineAwardHasNoCloudAllocation(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, false)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := s.AsOperator().CloudAwardResources(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil || allocation != nil {
		t.Fatalf("customer machine received Cloud allocation: %+v %v", allocation, err)
	}
}
