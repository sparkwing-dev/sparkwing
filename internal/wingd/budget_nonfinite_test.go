package wingd

import "testing"

func TestParseBudget_RefusesACapItCannotStandBehind(t *testing.T) {
	for _, raw := range []string{"nancores", "infcores", "nan%", "nan", "inf"} {
		budget, err := ParseBudget(raw)
		if err == nil {
			t.Errorf("ParseBudget(%q) = %+v, want an error: a cap that is not a number passes every bound written as a comparison, so HasCap reads false and the budget the operator typed silently grants the whole machine with enforcement off",
				raw, budget)
		}
	}
	if _, err := ParseBudget("2cores"); err != nil {
		t.Errorf("ParseBudget(\"2cores\") errored (%v); a real core count must still parse", err)
	}
}
