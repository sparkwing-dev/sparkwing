package wingd

import "testing"

func TestParseBudget_RefusesACapItCannotStandBehind(t *testing.T) {
	for _, raw := range []string{
		"nancores", "infcores", "nan%", "inf%", "nan", "inf",
		// every unit the memory path accepts reaches the same parse, and a cap
		// the operator typed for memory goes missing the same silent way.
		"nanmb", "infgb", "nankib", "inft", "nanb", "infkb",
	} {
		budget, err := ParseBudget(raw)
		if err == nil {
			t.Errorf("ParseBudget(%q) = %+v, want an error: a cap that is not a number passes every bound written as a comparison, so HasCap reads false and the budget the operator typed silently grants the whole machine with enforcement off",
				raw, budget)
		}
	}
	for _, raw := range []string{"2cores", "4gb", "50%", "512mib"} {
		if _, err := ParseBudget(raw); err != nil {
			t.Errorf("ParseBudget(%q) errored (%v); a cap the daemon can hold a run to must still parse", raw, err)
		}
	}
}
