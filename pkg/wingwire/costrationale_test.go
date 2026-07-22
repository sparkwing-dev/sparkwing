package wingwire_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestCostRationale_PhrasesEachSource(t *testing.T) {
	cases := []struct {
		name    string
		source  wingwire.CostSource
		samples int
		want    string
	}{
		{"pin", wingwire.CostSourcePin, 0, "explicit pin"},
		{"measured with samples", wingwire.CostSourceMeasured, 12, "measured p95 over 12 runs"},
		{"measured without samples", wingwire.CostSourceMeasured, 0, "measured p95"},
		{"measuring", wingwire.CostSourceMeasuring, 0, "re-measuring at prior charge"},
		{"floor", wingwire.CostSourceFloor, 0, "measuring up from the demand floor of contended runs"},
		{"default", wingwire.CostSourceDefault, 0, "first run, conservative default until measured"},
		{"unknown", wingwire.CostSource("weird"), 5, ""},
		{"empty", wingwire.CostSource(""), 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wingwire.CostRationale(tc.source, tc.samples); got != tc.want {
				t.Fatalf("CostRationale(%q, %d) = %q, want %q", tc.source, tc.samples, got, tc.want)
			}
		})
	}
}
