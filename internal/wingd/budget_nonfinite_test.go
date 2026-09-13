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
		// exactly the byte count that narrows to the most negative int64, which a
		// bound one step looser admits and every larger value in the list misses.
		"8589934592gib",
	} {
		budget, err := ParseBudget(raw)
		if err == nil {
			t.Errorf("ParseBudget(%q) = %+v, want an error: a cap no comparison orders, and a cap no byte count holds, both pass a bound written as a refusal, and the budget the operator typed then reads as no cap at all or as a negative reserve",
				raw, budget)
		}
	}
	for _, raw := range []string{"2cores", "8gb", "50%", "512mib", "4,8gb", "8589934591gib"} {
		if _, err := ParseBudget(raw); err != nil {
			t.Errorf("ParseBudget(%q) errored (%v); a cap the daemon can hold a run to must still parse", raw, err)
		}
	}
}
