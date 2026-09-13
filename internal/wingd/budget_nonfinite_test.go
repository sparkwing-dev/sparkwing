package wingd

import "testing"

func TestParseBudget_RefusesACapItCannotStandBehind(t *testing.T) {
	for _, raw := range []string{
		"nancores", "infcores", "-infcores", "nan", "inf",
		"nan%", "inf%", "-inf%",
		"nangb", "infgb", "nanmb", "infinitygb",
		// a finite size overflows the byte count the cluster layer narrows to an
		// int64, and a finite size below one byte rounds to no cap at all.
		"1e10gb", "17179869184gb", "0.0000001kb",
	} {
		budget, err := ParseBudget(raw)
		if err == nil {
			t.Errorf("ParseBudget(%q) = %+v, want an error: a cap that is not a number passes every bound written as a comparison, so HasCap reads false and the budget the operator typed silently grants the whole machine with enforcement off",
				raw, budget)
		}
	}
	for _, raw := range []string{"2cores", "8gb", "50%", "512mib", "4,8gb"} {
		if _, err := ParseBudget(raw); err != nil {
			t.Errorf("ParseBudget(%q) errored (%v); a cap the daemon can hold a run to must still parse", raw, err)
		}
	}
}
