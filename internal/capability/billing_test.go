package capability_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOperationalBillingClassification(t *testing.T) {
	want := map[capability.UnitKind]capability.BillingUnit{
		capability.UnitTrigger: capability.BillingHost,
		capability.UnitNode:    capability.BillingJob,
		capability.UnitPlan:    capability.BillingPlan,
		capability.UnitEval:    capability.BillingEval,
	}
	for _, mapping := range capability.UnitBillingMappings() {
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
