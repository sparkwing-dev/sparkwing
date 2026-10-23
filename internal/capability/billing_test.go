package capability

import (
	"testing"
)

func TestOperationalBillingClassification(t *testing.T) {
	want := map[UnitKind]BillingUnit{
		UnitTrigger: BillingHost,
		UnitNode:    BillingJob,
		UnitPlan:    BillingPlan,
		UnitEval:    BillingEval,
	}
	for _, mapping := range UnitBillingMappings() {
		billing, ok := want[mapping.Operational]
		if !ok || billing != mapping.Billing {
			t.Errorf("invalid or duplicate billing mapping: %+v", mapping)
		}
		delete(want, mapping.Operational)
	}
	if len(want) != 0 {
		t.Fatalf("unmapped operational units: %v", want)
	}
}
