package cronspec

import "time"

// MinCatchUp is the floor Decide applies to its catch-up window, so a timer
// that fires a few seconds late never reports a miss.
const MinCatchUp = 2 * time.Minute

// MaxMissed caps the backlog Decide counts.
const MaxMissed = 10000

// Decision is the outcome of evaluating a schedule at a tick.
type Decision struct {
	// Due is the latest due instant at or before now, and the cursor the
	// caller persists whether or not it fired. It is the zero time when
	// nothing came due.
	Due time.Time

	// Fire reports whether Due should run now.
	Fire bool

	// Missed counts the due instants in (cursor, now] that will not run:
	// everything before Due, plus Due itself when Fire is false. Counting
	// stops at MaxMissed, so a larger backlog reports exactly that.
	Missed int
}

// Decide evaluates one tick of s in loc. cursor is the last due instant already
// resolved - fired, skipped, or missed - and is the arming time for a schedule
// that has never been evaluated; every due instant in (cursor, now] is
// considered. The latest one fires when now.Sub(due) is within catchUp, which
// is raised to MinCatchUp when smaller. A cursor that already equals the latest
// due instant yields the zero Decision, so a tick never re-fires it.
//
// Whether a schedule is paused, and what to do about an overlapping run, are
// the caller's concerns.
func Decide(s *Schedule, loc *time.Location, cursor, now time.Time, catchUp time.Duration) Decision {
	if s == nil {
		return Decision{}
	}
	loc = zone(loc)
	if catchUp < MinCatchUp {
		catchUp = MinCatchUp
	}
	due := s.previous(now, loc)
	if due.IsZero() || !due.After(cursor) {
		return Decision{}
	}
	d := Decision{Due: due, Fire: now.Sub(due) <= catchUp}
	for at := cursor; d.Missed < MaxMissed; {
		next := s.Next(at, loc)
		if next.IsZero() || !next.Before(due) {
			break
		}
		d.Missed++
		at = next
	}
	if !d.Fire && d.Missed < MaxMissed {
		d.Missed++
	}
	return d
}
