package crons

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TickStaleAfter is how long a host with an enabled timer may go without a
// tick before the tick counts as stale. It is three minutes because the timer
// fires every minute and a busy host may lose one.
const TickStaleAfter = 3 * time.Minute

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

// Healthy reports whether this host is evaluating what it armed. A host with
// nothing armed is healthy: there is nothing for the timer to do. A recent
// tick counts as evidence on its own, so a host driving the tick from its own
// scheduler passes without a sparkwing timer. A tick that landed but reported
// a failure is not health: something armed here was not evaluated.
func (h Health) Healthy() bool {
	if h.Armed == 0 {
		return true
	}
	if h.TickStale || h.Timer.Foreign || h.Timer.Stale || h.LastTick.Error != "" {
		return false
	}
	return h.Timer.Enabled || !h.LastTick.At.IsZero()
}

// Health reads the timer and the store and says whether this host is
// evaluating what it armed. A timer the service manager cannot be asked about
// is reported through the returned Health rather than as an error, because the
// counts and the last tick still answer the question.
func (s *Service) Health(ctx context.Context, timer crontimer.Host) (Health, error) {
	rows, err := s.List(ctx)
	if err != nil {
		return Health{}, err
	}
	health := Health{Schedules: len(rows)}
	for _, r := range rows {
		health.count(r)
	}

	tick, err := s.Store.GetCronTick(ctx)
	if err != nil {
		return health, err
	}
	health.LastTick = tick

	state, terr := crontimer.Status(timer)
	if terr != nil && !errors.Is(terr, crontimer.ErrUnsupported) {
		return health, terr
	}
	health.Timer = state
	if errors.Is(terr, crontimer.ErrUnsupported) {
		health.Timer.Detail = terr.Error()
	}
	now := s.now()
	health.TickStale = (state.Enabled || !tick.At.IsZero()) && now.Sub(tick.At) > TickStaleAfter
	health.Detail = health.describe(now)
	health.Remedy = health.remedy()
	return health, nil
}

func (h *Health) count(r Row) {
	switch r.State {
	case StateArmed:
		h.Armed++
	case StatePaused:
		h.Paused++
	case StateUndeclared:
		h.Undeclared++
	}
	switch r.Lock.State {
	case LockFollows:
		h.Following++
	case LockAhead:
		h.Locked++
		h.Ahead++
	case LockMissing:
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
	return strings.Join(parts, "; ") + "; re-run `sparkwing crons install` to pin the checkout as it stands"
}

func (h Health) describe(now time.Time) string {
	switch {
	case h.Timer.Foreign:
		return fmt.Sprintf("%s exists but sparkwing did not write it, so the tick is whatever that file runs", h.Timer.Path)
	case h.Armed == 0 && h.Schedules == 0:
		return "nothing is armed here; `sparkwing crons install` arms this repository's schedules"
	case h.Armed == 0:
		return fmt.Sprintf("no schedule is armed here (%d paused, %d undeclared)", h.Paused, h.Undeclared)
	case !h.Timer.Installed && !h.LastTick.At.IsZero() && !h.TickStale:
		return fmt.Sprintf("no sparkwing timer is installed here, and something ticked %s ago, so %d schedule(s) are being evaluated",
			roundDuration(now.Sub(h.LastTick.At)), h.Armed)
	case !h.Timer.Installed:
		return fmt.Sprintf("%d schedule(s) are armed but no timer runs the tick; `sparkwing crons install` writes one", h.Armed)
	case !h.Timer.Enabled:
		return fmt.Sprintf("the timer is installed but not running, so the %d armed schedule(s) never fire", h.Armed)
	case h.Timer.Stale:
		return fmt.Sprintf("the timer runs %s, which is not this sparkwing; `sparkwing crons install` repoints it", h.Timer.Binary)
	case h.TickStale && h.LastTick.At.IsZero():
		return "the timer is enabled but has never ticked; the log at crons.log says why"
	case h.TickStale:
		return fmt.Sprintf("the last tick was %s ago, so the timer is enabled but not landing; the log at crons.log says why",
			roundDuration(now.Sub(h.LastTick.At)))
	case h.LastTick.Error != "":
		return "the last tick reported: " + h.LastTick.Error
	}
	return fmt.Sprintf("%d schedule(s) armed and the timer ticks every minute", h.Armed)
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
