package capability_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
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

func TestBillingMinimumsUseLimits(t *testing.T) {
	want := map[capability.BillingUnit]int64{
		capability.BillingPlan: 0,
		capability.BillingEval: 0,
		capability.BillingJob:  int64(limits.WorkBillingMinimum / time.Second),
		capability.BillingHost: int64(limits.HostBillingMinimum / time.Second),
	}
	for _, rule := range capability.BillingRules() {
		minimum, ok := want[rule.Unit]
		if !ok || rule.MinimumSeconds != minimum || !rule.PerSecond || !rule.ExcludeCustomerRunnerWait {
			t.Errorf("invalid billing rule: %+v", rule)
		}
		if rule.CacheHitFree != (rule.Unit == capability.BillingPlan) {
			t.Errorf("incorrect cache billing for %s", rule.Unit)
		}
		delete(want, rule.Unit)
	}
	if len(want) != 0 {
		t.Fatalf("missing billing rules: %v", want)
	}
}
