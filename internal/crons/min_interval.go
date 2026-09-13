package crons

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: ten fires cover the repeating step of every cadence a minute field
// can express, so the shortest gap measured over them is the schedule's own.
const minIntervalSamples = 10

// RefuseBelowMinInterval reports the compute guard a schedule's cadence
// breaks, or nil when it is at or above the guard. A ceiling of zero is
// unlimited, and an expression that resolves fewer than two instants is
// measured as no cadence at all.
func RefuseBelowMinInterval(expr string, ceilingSeconds int64) error {
	if expr == "" || ceilingSeconds <= 0 {
		return nil
	}
	schedule := parsedOrNil(expr)
	if schedule == nil {
		return nil
	}
	shortest, ok := schedule.ShortestInterval(time.Now().UTC(), time.UTC, minIntervalSamples)
	if !ok {
		return nil
	}
	seconds := int64(shortest.Seconds())
	if seconds >= ceilingSeconds {
		return nil
	}
	return &store.ComputeLimitError{
		Limit: store.ComputeLimitCronSeconds, Cap: ceilingSeconds,
		Observed: seconds, Scope: "schedule " + expr,
	}
}

// safety: an expression that does not parse is the cron layer's own failure,
// reported where the schedule is armed and evaluated rather than here.
func parsedOrNil(expr string) *cronspec.Schedule {
	schedule, err := cronspec.Parse(expr)
	if err != nil {
		return nil
	}
	return schedule
}
