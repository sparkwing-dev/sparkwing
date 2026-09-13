// Package objectguard bounds how many requests a process may send to an
// object store.
//
// One Limiter per process counts PUT, GET, LIST and DELETE separately,
// against a per-minute rate and a per-day budget each. Exceeding either
// budget trips that class: every further request of that class is
// refused with a *BudgetError and nothing is queued. Classes are
// independent, so a tripped PUT budget leaves reads working on their
// own budget.
//
// The guard exists because a retry loop against a failing bucket bills
// per request, and the bill arrives long after the loop starts.
package objectguard

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Class is an object-store request class, matching how object stores
// price requests.
type Class string

const (
	// ClassPut counts object writes.
	ClassPut Class = "put"
	// ClassGet counts object reads, including HEAD, which stores bill
	// at the GET rate.
	ClassGet Class = "get"
	// ClassList counts bucket listings.
	ClassList Class = "list"
	// ClassDelete counts object deletions.
	ClassDelete Class = "delete"
)

// Classes lists every counted class in a stable order.
func Classes() []Class { return []Class{ClassPut, ClassGet, ClassList, ClassDelete} }

// IsWrite reports whether a class mutates the store. Write classes fail
// closed the moment their budget trips; read classes keep serving until
// their own budget trips.
func (c Class) IsWrite() bool { return c == ClassPut || c == ClassDelete }

// Window names the budget a refusal came from.
type Window string

const (
	// WindowMinute is the per-minute rate budget.
	WindowMinute Window = "minute"
	// WindowDay is the per-day request budget.
	WindowDay Window = "day"
)

// ErrBudgetExceeded matches every *BudgetError under errors.Is.
var ErrBudgetExceeded = errors.New("object-store request budget exceeded")

// BudgetError reports a refused object-store request. It names the
// class, the budget that tripped, and its limit so an operator can act
// on the message alone.
type BudgetError struct {
	Class  Class
	Window Window
	Limit  int
	// Tripped is when the class entered its tripped state.
	Tripped time.Time
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf(
		"object-store %s budget exceeded: the per-%s limit of %d %s requests is spent, so the breaker tripped at %s and refuses further %s requests; clear it with `sparkwing cluster object-store reset-breaker` or raise SPARKWING_OBJECT_STORE_%s_PER_%s",
		e.Class, e.Window, e.Limit, e.Class,
		e.Tripped.UTC().Format(time.RFC3339), e.Class,
		upper(string(e.Class)), upper(string(e.Window)),
	)
}

func (e *BudgetError) Unwrap() error { return ErrBudgetExceeded }

func upper(s string) string {
	out := []byte(s)
	for i, b := range out {
		if b >= 'a' && b <= 'z' {
			out[i] = b - ('a' - 'A')
		}
	}
	return string(out)
}

// Limit is one class's pair of budgets. A zero or negative value means
// that budget does not apply.
type Limit struct {
	PerMinute int
	PerDay    int
}

// TripReset says what clears a tripped class without an operator.
type TripReset string

const (
	// TripResetDay clears a tripped class when its day window rolls.
	TripResetDay TripReset = "day"
	// TripResetManual keeps a class tripped until an explicit reset.
	TripResetManual TripReset = "manual"
)

// Config is a Limiter's whole configuration.
type Config struct {
	// Enabled false lets every request through and still counts it,
	// which is the local escape hatch for an operator who needs a
	// process to finish past a tripped budget.
	Enabled bool
	Limits  map[Class]Limit
	Reset   TripReset
}

// Default budgets. The per-minute rates sit an order of magnitude above
// a busy controller, which writes one object per log append, and far
// below a retry loop with no sleep, which issues thousands of requests
// a second and so trips in under two seconds. Each day budget is about
// 150 times its minute rate, roughly two and a half hours at sustained
// peak, so a leak that stays under the minute rate is still caught the
// same day.
const (
	DefaultPutPerMinute = 1200
	DefaultPutPerDay    = 200000

	DefaultGetPerMinute = 3000
	DefaultGetPerDay    = 500000

	DefaultListPerMinute = 600
	DefaultListPerDay    = 100000

	DefaultDeletePerMinute = 600
	DefaultDeletePerDay    = 100000
)

// DefaultConfig returns the built-in budgets.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Reset:   TripResetDay,
		Limits: map[Class]Limit{
			ClassPut:    {PerMinute: DefaultPutPerMinute, PerDay: DefaultPutPerDay},
			ClassGet:    {PerMinute: DefaultGetPerMinute, PerDay: DefaultGetPerDay},
			ClassList:   {PerMinute: DefaultListPerMinute, PerDay: DefaultListPerDay},
			ClassDelete: {PerMinute: DefaultDeletePerMinute, PerDay: DefaultDeletePerDay},
		},
	}
}

// ClassState is one class's counters and trip state at a point in time.
type ClassState struct {
	Class         Class     `json:"class"`
	PerMinute     int       `json:"per_minute"`
	PerDay        int       `json:"per_day"`
	MinuteUsed    int       `json:"minute_used"`
	DayUsed       int       `json:"day_used"`
	Allowed       uint64    `json:"allowed_total"`
	Refused       uint64    `json:"refused_total"`
	Trips         uint64    `json:"trips_total"`
	Tripped       bool      `json:"tripped"`
	TrippedAt     time.Time `json:"tripped_at,omitzero"`
	TrippedWindow Window    `json:"tripped_window,omitempty"`
}

// State is the whole limiter at a point in time, as the health route
// and the metrics collector report it.
type State struct {
	Enabled bool         `json:"enabled"`
	Reset   TripReset    `json:"reset"`
	Tripped bool         `json:"tripped"`
	Classes []ClassState `json:"classes"`
}

type classCounters struct {
	limit Limit

	minuteStart time.Time
	minuteUsed  int
	dayStart    time.Time
	dayUsed     int

	allowed uint64
	refused uint64
	trips   uint64

	tripped       bool
	trippedAt     time.Time
	trippedWindow Window
}

// Limiter counts object-store requests per class and refuses the ones
// past budget. Safe for concurrent use.
type Limiter struct {
	mu       sync.Mutex
	enabled  bool
	reset    TripReset
	counters map[Class]*classCounters
	now      func() time.Time
}

// New builds a Limiter from cfg. A class missing from cfg.Limits is
// unbudgeted and always allowed.
func New(cfg Config) *Limiter {
	l := &Limiter{
		enabled:  cfg.Enabled,
		reset:    cfg.Reset,
		counters: make(map[Class]*classCounters, len(Classes())),
		now:      time.Now,
	}
	if l.reset == "" {
		l.reset = TripResetDay
	}
	for _, c := range Classes() {
		l.counters[c] = &classCounters{limit: cfg.Limits[c]}
	}
	return l
}

// Allow records one request of class c and reports whether it may
// proceed. A refusal returns a *BudgetError and consumes no budget,
// because the request never reaches the store.
func (l *Limiter) Allow(c Class) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.counters[c]
	if !ok {
		st = &classCounters{}
		l.counters[c] = st
	}
	now := l.now().UTC()
	l.rollWindows(st, now)

	if st.tripped && l.enabled {
		st.refused++
		return &BudgetError{Class: c, Window: st.trippedWindow, Limit: l.limitFor(st, st.trippedWindow), Tripped: st.trippedAt}
	}
	if window, limit, over := overBudget(st); over {
		if !st.tripped {
			st.tripped = true
			st.trippedAt = now
			st.trippedWindow = window
			st.trips++
		}
		if l.enabled {
			st.refused++
			return &BudgetError{Class: c, Window: window, Limit: limit, Tripped: st.trippedAt}
		}
	}
	st.minuteUsed++
	st.dayUsed++
	st.allowed++
	return nil
}

func overBudget(st *classCounters) (Window, int, bool) {
	if st.limit.PerMinute > 0 && st.minuteUsed >= st.limit.PerMinute {
		return WindowMinute, st.limit.PerMinute, true
	}
	if st.limit.PerDay > 0 && st.dayUsed >= st.limit.PerDay {
		return WindowDay, st.limit.PerDay, true
	}
	return "", 0, false
}

func (l *Limiter) limitFor(st *classCounters, w Window) int {
	if w == WindowDay {
		return st.limit.PerDay
	}
	return st.limit.PerMinute
}

func (l *Limiter) rollWindows(st *classCounters, now time.Time) {
	if st.minuteStart.IsZero() || now.Sub(st.minuteStart) >= time.Minute {
		st.minuteStart = now.Truncate(time.Minute)
		st.minuteUsed = 0
	}
	day := now.Truncate(24 * time.Hour)
	if st.dayStart.IsZero() {
		st.dayStart = day
		return
	}
	if day.After(st.dayStart) {
		st.dayStart = day
		st.dayUsed = 0
		if l.reset == TripResetDay {
			st.tripped = false
			st.trippedWindow = ""
		}
	}
}

// Reset clears every class's tripped state and both window counters,
// which is what the operator reset verb performs. Lifetime totals and
// the trip count survive so the metrics keep their history.
func (l *Limiter) Reset() {
	for _, c := range Classes() {
		l.ResetClass(c)
	}
}

// ResetClass clears one class's tripped state and window counters. It
// reports whether the class was tripped.
func (l *Limiter) ResetClass(c Class) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.counters[c]
	if !ok {
		return false
	}
	was := st.tripped
	st.tripped = false
	st.trippedWindow = ""
	st.trippedAt = time.Time{}
	st.minuteUsed = 0
	st.dayUsed = 0
	return was
}

// Tripped reports whether any class is currently tripped.
func (l *Limiter) Tripped() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, st := range l.counters {
		if st.tripped {
			return true
		}
	}
	return false
}

// State snapshots the limiter for the health route and metrics.
func (l *Limiter) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UTC()
	out := State{Enabled: l.enabled, Reset: l.reset}
	for _, c := range Classes() {
		st := l.counters[c]
		if st == nil {
			continue
		}
		l.rollWindows(st, now)
		if st.tripped {
			out.Tripped = true
		}
		out.Classes = append(out.Classes, ClassState{
			Class:         c,
			PerMinute:     st.limit.PerMinute,
			PerDay:        st.limit.PerDay,
			MinuteUsed:    st.minuteUsed,
			DayUsed:       st.dayUsed,
			Allowed:       st.allowed,
			Refused:       st.refused,
			Trips:         st.trips,
			Tripped:       st.tripped,
			TrippedAt:     st.trippedAt,
			TrippedWindow: st.trippedWindow,
		})
	}
	return out
}
