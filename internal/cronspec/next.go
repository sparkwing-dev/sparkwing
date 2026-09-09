package cronspec

import "time"

// safety: bounds every search, so an expression that can never match (Feb 30) terminates.
const yearHorizon = 5

// Next returns the first instant strictly after `after` that matches s,
// evaluated as wall-clock time in loc and truncated to the minute. A nil loc
// means UTC.
//
// When both day-of-month and day-of-week are restricted, a day matches if
// either field matches, as standard cron does. A field counts as restricted
// only when it does not begin with `*`, which is the flag Vixie cron sets, so
// `0 3 */2 * 1` means every second day and every Monday, not their
// intersection.
//
// Across a daylight-saving transition the walk follows the wall clock: a minute
// that does not exist on a spring-forward day is skipped, and a minute that
// occurs twice on a fall-back day matches once, at its first occurrence.
//
// Next returns the zero time when nothing matches within five years.
func (s *Schedule) Next(after time.Time, loc *time.Location) time.Time {
	if s == nil {
		return time.Time{}
	}
	loc = zone(loc)
	local := after.In(loc)
	w := walk{local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute()}
	w.addMinute()
	limit := w.year + yearHorizon
	for w.year <= limit {
		switch {
		case !bitSet(s.month, int(w.month)):
			w.nextMonth()
		case !s.dayMatches(w.year, w.month, w.day):
			w.nextDay()
		case !bitSet(s.hour, w.hour):
			w.nextHour()
		case !bitSet(s.minute, w.minute):
			w.addMinute()
		default:
			if when, ok := w.resolve(loc); ok && when.After(after) {
				return when
			}
			w.addMinute()
		}
	}
	return time.Time{}
}

// Upcoming returns the next n instants after `after` that match s, in loc. It
// returns fewer than n, possibly none, when the five-year horizon runs out.
func (s *Schedule) Upcoming(after time.Time, loc *time.Location, n int) []time.Time {
	if s == nil || n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, n)
	for at := after; len(out) < n; {
		next := s.Next(at, loc)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		at = next
	}
	return out
}

func (s *Schedule) previous(at time.Time, loc *time.Location) time.Time {
	if s == nil {
		return time.Time{}
	}
	loc = zone(loc)
	local := at.In(loc)
	w := walk{local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute()}
	limit := w.year - yearHorizon
	for w.year >= limit {
		switch {
		case !bitSet(s.month, int(w.month)):
			w.prevMonth()
		case !s.dayMatches(w.year, w.month, w.day):
			w.prevDay()
		case !bitSet(s.hour, w.hour):
			w.prevHour()
		case !bitSet(s.minute, w.minute):
			w.subMinute()
		default:
			if when, ok := w.resolve(loc); ok && !when.After(at) {
				return when
			}
			w.subMinute()
		}
	}
	return time.Time{}
}

func (s *Schedule) dayMatches(year int, month time.Month, day int) bool {
	domOK := bitSet(s.dom, day)
	dowOK := bitSet(s.dow, int(weekdayOf(year, month, day)))
	if s.domRestricted && s.dowRestricted {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// perf: stepping one field at a time lets a sparse expression skip whole months.
type walk struct {
	year   int
	month  time.Month
	day    int
	hour   int
	minute int
}

// safety: time.Date normalizes a spring-forward gap forward, which shows up as fields that no
// longer match; a minute repeated by a fall-back may come back as either occurrence, so this
// steps to the first one.
func (w *walk) resolve(loc *time.Location) (time.Time, bool) {
	t := time.Date(w.year, w.month, w.day, w.hour, w.minute, 0, 0, loc)
	if !w.holds(t) {
		return time.Time{}, false
	}
	_, offset := t.Zone()
	if _, before := t.Add(-24 * time.Hour).Zone(); before > offset {
		if earlier := t.Add(-time.Duration(before-offset) * time.Second); w.holds(earlier) {
			return earlier, true
		}
	}
	return t, true
}

func (w *walk) holds(t time.Time) bool {
	return t.Year() == w.year && t.Month() == w.month && t.Day() == w.day &&
		t.Hour() == w.hour && t.Minute() == w.minute
}

func (w *walk) addMinute() {
	w.minute++
	if w.minute > 59 {
		w.minute = 0
		w.hour++
	}
	if w.hour > 23 {
		w.hour = 0
		w.day++
	}
	w.carryDay()
}

func (w *walk) nextHour() {
	w.minute = 0
	w.hour++
	if w.hour > 23 {
		w.hour = 0
		w.day++
		w.carryDay()
	}
}

func (w *walk) nextDay() {
	w.minute, w.hour = 0, 0
	w.day++
	w.carryDay()
}

func (w *walk) nextMonth() {
	w.minute, w.hour, w.day = 0, 0, 1
	w.month++
	if w.month > time.December {
		w.month = time.January
		w.year++
	}
}

func (w *walk) carryDay() {
	if w.day > daysIn(w.year, w.month) {
		w.day = 1
		w.month++
		if w.month > time.December {
			w.month = time.January
			w.year++
		}
	}
}

func (w *walk) subMinute() {
	w.minute--
	if w.minute < 0 {
		w.minute = 59
		w.hour--
	}
	if w.hour < 0 {
		w.hour = 23
		w.day--
	}
	w.borrowDay()
}

func (w *walk) prevHour() {
	w.minute = 59
	w.hour--
	if w.hour < 0 {
		w.hour = 23
		w.day--
		w.borrowDay()
	}
}

func (w *walk) prevDay() {
	w.minute, w.hour = 59, 23
	w.day--
	w.borrowDay()
}

func (w *walk) prevMonth() {
	w.minute, w.hour = 59, 23
	w.month--
	if w.month < time.January {
		w.month = time.December
		w.year--
	}
	w.day = daysIn(w.year, w.month)
}

func (w *walk) borrowDay() {
	if w.day < 1 {
		w.month--
		if w.month < time.January {
			w.month = time.December
			w.year--
		}
		w.day = daysIn(w.year, w.month)
	}
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func weekdayOf(year int, month time.Month, day int) time.Weekday {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Weekday()
}
