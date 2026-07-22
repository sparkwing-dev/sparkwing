package wingwire

import "fmt"

// CostRationale returns a short parenthetical phrase explaining where a
// resolved host charge came from, keyed on its [CostSource]. It is the one
// canonical wording every surface shares -- the waiting run's log line, the
// queue view's holder and waiter rows, and the JSON a dashboard tooltips --
// so "needs 5.0 cores" never appears without its why. sampleCount enriches
// the measured phrase with how many runs back the price and is ignored for
// the other sources. It returns "" for an unknown or empty source so callers
// can omit the annotation entirely.
func CostRationale(source CostSource, sampleCount int) string {
	switch source {
	case CostSourcePin:
		return "explicit pin"
	case CostSourceMeasured:
		if sampleCount > 0 {
			return fmt.Sprintf("measured p95 over %d runs", sampleCount)
		}
		return "measured p95"
	case CostSourceMeasuring:
		return "re-measuring at prior charge"
	case CostSourceFloor:
		return "measuring up from the demand floor of contended runs"
	case CostSourceDefault:
		return "first run, conservative default until measured"
	default:
		return ""
	}
}
