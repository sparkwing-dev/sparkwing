package crons

import (
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// tickStaleAfter is how long a host with an enabled timer may go without a
// tick before the tick counts as stale. It is three minutes because the timer
// fires every minute and a busy host may lose one.
const tickStaleAfter = 3 * time.Minute

// Health is the host-level view of the scheduler: the OS timer, the last tick,
// and what this home has armed.
type Health struct {
	Timer      crontimer.State `json:"timer"`
	LastTick   store.CronTick  `json:"last_tick"`
	TickStale  bool            `json:"tick_stale"`
	Schedules  int             `json:"schedules"`
	Armed      int             `json:"armed"`
	Paused     int             `json:"paused"`
	Undeclared int             `json:"undeclared"`
	// Locked counts the schedules running a pinned pipeline binary, and
	// Following the ones compiling the checkout on every fire.
	Locked    int `json:"locked"`
	Following int `json:"following"`
	// Ahead counts the pinned schedules whose checkout has moved on, and
	// MissingBinary the ones whose pinned binary is gone.
	Ahead         int `json:"ahead"`
	MissingBinary int `json:"missing_binary"`
	// StaleOverride counts the schedules whose declaration moved under this
	// host's override since it was set.
	StaleOverride int    `json:"stale_override"`
	Detail        string `json:"detail"`
	// Remedy names what to run when the counts above want attention, empty
	// when nothing does.
	Remedy string `json:"remedy,omitempty"`
}

func (h *Health) count(r Row) {
	switch r.State {
	case stateArmed:
		h.Armed++
	case statePaused:
		h.Paused++
	case stateUndeclared:
		h.Undeclared++
	}
	switch r.Lock.State {
	case lockFollows:
		h.Following++
	case lockAhead:
		h.Locked++
		h.Ahead++
	case lockMissing:
		h.Locked++
		h.MissingBinary++
	default:
		h.Locked++
	}
	if r.OverrideStale {
		h.StaleOverride++
	}
}

// safety: every drift here is answered by the same explicit act, because
// re-arming is what moves a pin and re-bases an override.
func (h Health) remedy() string {
	var parts []string
	if h.MissingBinary > 0 {
		parts = append(parts, fmt.Sprintf("%d pinned binary/binaries are gone", h.MissingBinary))
	}
	if h.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("%d schedule(s) are pinned behind the checkout", h.Ahead))
	}
	if h.StaleOverride > 0 {
		parts = append(parts, fmt.Sprintf("%d override(s) were set against a declaration that has moved", h.StaleOverride))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "; re-run `sparkwing schedules install` to pin the checkout as it stands"
}

func roundDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}
