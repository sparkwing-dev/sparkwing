package capability

import "testing"

func TestBillingMinimumsMatchContract(t *testing.T) {
	want := map[BillingUnit]int64{
		BillingPlan: 0,
		BillingEval: 0,
		BillingJob:  60,
		BillingHost: 60,
	}
	for _, rule := range billingRules {
		minimum, ok := want[rule.Unit]
		if !ok || rule.MinimumSeconds != minimum || !rule.PerSecond || !rule.ExcludeCustomerRunnerWait {
			t.Errorf("invalid billing rule: %+v", rule)
		}
		if rule.CacheHitFree != (rule.Unit == BillingPlan) {
			t.Errorf("incorrect cache billing for %s", rule.Unit)
		}
		delete(want, rule.Unit)
	}
	if len(want) != 0 {
		t.Fatalf("missing billing rules: %v", want)
	}
}
