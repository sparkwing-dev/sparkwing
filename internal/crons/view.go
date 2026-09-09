package crons

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: one wire shape for a host's schedules and a controller's, flat and
// stringly typed -- RFC3339 times, empty when unset, nanosecond durations -- so
// the JSON does not move when the Go types behind it do.

// TimerStateView is the OS timer that calls the tick. A controller reports its
// own loop here instead, with [ControllerTimerDetail] as the detail.
type TimerStateView struct {
	Installed bool   `json:"installed"`
	Foreign   bool   `json:"foreign"`
	Enabled   bool   `json:"enabled"`
	Stale     bool   `json:"stale"`
	Path      string `json:"path"`
	Binary    string `json:"binary"`
	Detail    string `json:"detail"`
}

// TickView is the last tick recorded against the store. At is empty until one
// has landed.
type TickView struct {
	At      string `json:"at"`
	Host    string `json:"host"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// HealthView is whether the evaluator is running and what it holds.
type HealthView struct {
	Timer         TimerStateView `json:"timer"`
	LastTick      TickView       `json:"last_tick"`
	TickStale     bool           `json:"tick_stale"`
	Schedules     int            `json:"schedules"`
	Armed         int            `json:"armed"`
	Paused        int            `json:"paused"`
	Undeclared    int            `json:"undeclared"`
	Locked        int            `json:"locked"`
	Following     int            `json:"following"`
	Ahead         int            `json:"ahead"`
	MissingBinary int            `json:"missing_binary"`
	StaleOverride int            `json:"stale_override"`
	Detail        string         `json:"detail"`
	Remedy        string         `json:"remedy"`
}

// LockView is what a schedule runs. State is one of [LockFollows],
// [LockPinned], [LockAhead], [LockDirty] or [LockMissing]; the other fields are
// empty while a schedule follows the checkout.
type LockView struct {
	Ref    string `json:"ref"`
	Binary string `json:"binary"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

// OverrideView names the declared fields the evaluating side has overridden,
// and whether the declaration has moved under them since.
type OverrideView struct {
	Fields []string `json:"fields"`
	Stale  bool     `json:"stale"`
	SetAt  string   `json:"set_at"`
}

// EffectiveView is the cadence a schedule actually runs.
type EffectiveView struct {
	Cron      string            `json:"cron"`
	TZ        string            `json:"tz"`
	Overlap   string            `json:"overlap"`
	CatchUpNS int64             `json:"catch_up_ns"`
	Args      map[string]string `json:"args"`
}

// ScheduleView is one schedule. Name is the display name; GitBranch is set
// only on a schedule pushed to a controller.
type ScheduleView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ScheduleName string            `json:"schedule_name"`
	RepoPath     string            `json:"repo_path"`
	Pipeline     string            `json:"pipeline"`
	Where        string            `json:"where"`
	GitBranch    string            `json:"git_branch,omitempty"`
	Cron         string            `json:"cron"`
	TZ           string            `json:"tz"`
	Overlap      string            `json:"overlap"`
	CatchUpNS    int64             `json:"catch_up_ns"`
	Args         map[string]string `json:"args"`
	Lock         LockView          `json:"lock"`
	Override     OverrideView      `json:"override"`
	Effective    EffectiveView     `json:"effective"`
	Paused       bool              `json:"paused"`
	Declared     bool              `json:"declared"`
	State        string            `json:"state"`
	StateDetail  string            `json:"state_detail"`
	ArmedAt      string            `json:"armed_at"`
	UpdatedAt    string            `json:"updated_at"`
	LastFiredAt  *string           `json:"last_fired_at"`
	LastRunID    string            `json:"last_run_id"`
	LastOutcome  string            `json:"last_outcome"`
	NextDueAt    *string           `json:"next_due_at"`
}

// FireView is one resolved due instant. RunStatus is empty when the fire
// launched nothing or its run has since been pruned.
type FireView struct {
	ID         string            `json:"id"`
	ScheduleID string            `json:"schedule_id"`
	DueAt      string            `json:"due_at"`
	DecidedAt  string            `json:"decided_at"`
	Outcome    string            `json:"outcome"`
	RunID      string            `json:"run_id"`
	Detail     string            `json:"detail"`
	Args       map[string]string `json:"args"`
	RunStatus  string            `json:"run_status"`
}

// OverviewView is the evaluator's health and everything it holds.
type OverviewView struct {
	Health    HealthView     `json:"health"`
	Schedules []ScheduleView `json:"schedules"`
}

// DetailView is one schedule, its recent fires newest first, and the next
// instants it matches.
type DetailView struct {
	Schedule ScheduleView `json:"schedule"`
	Fires    []FireView   `json:"fires"`
	Upcoming []string     `json:"upcoming"`
}

// ScheduleEnvelope is what a write returns: the schedule as it now stands.
type ScheduleEnvelope struct {
	Schedule ScheduleView `json:"schedule"`
}

// RunEnvelope is what a launch returns: the run it started and the schedule as
// it now stands.
type RunEnvelope struct {
	RunID    string       `json:"run_id"`
	Schedule ScheduleView `json:"schedule"`
}

// NewHealthView converts a [Health] for the wire.
func NewHealthView(h Health) HealthView {
	return HealthView{
		Timer:         newTimerStateView(h.Timer),
		LastTick:      newTickView(h),
		TickStale:     h.TickStale,
		Schedules:     h.Schedules,
		Armed:         h.Armed,
		Paused:        h.Paused,
		Undeclared:    h.Undeclared,
		Locked:        h.Locked,
		Following:     h.Following,
		Ahead:         h.Ahead,
		MissingBinary: h.MissingBinary,
		StaleOverride: h.StaleOverride,
		Detail:        h.Detail,
		Remedy:        h.Remedy,
	}
}

func newTimerStateView(t crontimer.State) TimerStateView {
	return TimerStateView{
		Installed: t.Installed,
		Foreign:   t.Foreign,
		Enabled:   t.Enabled,
		Stale:     t.Stale,
		Path:      t.Path,
		Binary:    t.Binary,
		Detail:    t.Detail,
	}
}

func newTickView(h Health) TickView {
	return TickView{
		At:      RFC3339(h.LastTick.At),
		Host:    h.LastTick.Host,
		Version: h.LastTick.Version,
		Error:   h.LastTick.Error,
	}
}

// NewScheduleView converts a [Row] for the wire.
func NewScheduleView(row Row) ScheduleView {
	override := OverrideView{Fields: row.OverrideFields, Stale: row.OverrideStale}
	if override.Fields == nil {
		override.Fields = []string{}
	}
	if row.Override != nil {
		override.SetAt = RFC3339(row.Override.SetAt)
	}
	return ScheduleView{
		ID:           row.ID,
		Name:         row.Display,
		ScheduleName: row.ScheduleName,
		RepoPath:     row.RepoPath,
		Pipeline:     row.Pipeline,
		Where:        row.Where,
		GitBranch:    row.GitBranch,
		Cron:         row.Cron,
		TZ:           row.TZ,
		Overlap:      row.Overlap,
		CatchUpNS:    int64(row.CatchUp),
		Args:         ViewArgs(row.Args),
		Lock: LockView{
			Ref:    row.Lock.Ref,
			Binary: row.Lock.Binary,
			Digest: row.Lock.Digest,
			State:  row.Lock.State,
		},
		Override: override,
		Effective: EffectiveView{
			Cron:      row.Effective.Cron,
			TZ:        row.Effective.TZ,
			Overlap:   row.Effective.Overlap,
			CatchUpNS: int64(row.Effective.CatchUp),
			Args:      ViewArgs(row.Effective.Args),
		},
		Paused:      row.Paused,
		Declared:    row.Declared,
		State:       row.State,
		StateDetail: row.StateDetail,
		ArmedAt:     RFC3339(row.ArmedAt),
		UpdatedAt:   RFC3339(row.UpdatedAt),
		LastFiredAt: rfc3339Ptr(row.LastFiredAt),
		LastRunID:   row.LastRunID,
		LastOutcome: row.LastOutcome,
		NextDueAt:   rfc3339Ptr(row.NextDueAt),
	}
}

// NewFireView converts one fire for the wire, joined with the current status of
// the run it launched.
func NewFireView(fire store.CronFire, runStatus string) FireView {
	return FireView{
		ID:         fire.ID,
		ScheduleID: fire.ScheduleID,
		DueAt:      RFC3339(fire.DueAt),
		DecidedAt:  RFC3339(fire.DecidedAt),
		Outcome:    fire.Outcome,
		RunID:      fire.RunID,
		Detail:     fire.Detail,
		Args:       ViewArgs(fire.Args),
		RunStatus:  runStatus,
	}
}

// ViewArgs renders an argument set for a reader that indexes it directly, so a
// schedule with no arguments serves an empty object rather than a null.
func ViewArgs(args map[string]string) map[string]string {
	if args == nil {
		return map[string]string{}
	}
	return args
}

// RFC3339 renders an instant in UTC, empty when it is unset.
func RFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := RFC3339(*t)
	return &s
}

// RowFromView rebuilds the [Row] a renderer reads from the wire shape a remote
// evaluator served, so one renderer answers for both sides. The fields the
// wire does not carry -- who armed the schedule, and its cursor -- come back
// zero.
func RowFromView(v ScheduleView) Row {
	row := Row{
		CronSchedule: store.CronSchedule{
			ID:           v.ID,
			RepoPath:     v.RepoPath,
			Pipeline:     v.Pipeline,
			Name:         v.ScheduleName,
			Cron:         v.Cron,
			TZ:           v.TZ,
			Overlap:      v.Overlap,
			CatchUp:      time.Duration(v.CatchUpNS),
			Where:        v.Where,
			GitBranch:    v.GitBranch,
			Args:         emptyToNil(v.Args),
			LockedRef:    v.Lock.Ref,
			LockedBinary: v.Lock.Binary,
			LockedDigest: v.Lock.Digest,
			Paused:       v.Paused,
			Declared:     v.Declared,
			ArmedAt:      parseRFC3339(v.ArmedAt),
			UpdatedAt:    parseRFC3339(v.UpdatedAt),
			LastFiredAt:  parseRFC3339Ptr(v.LastFiredAt),
			LastRunID:    v.LastRunID,
			LastOutcome:  v.LastOutcome,
			NextDueAt:    parseRFC3339Ptr(v.NextDueAt),
		},
		Display:      v.Name,
		ScheduleName: v.ScheduleName,
		State:        v.State,
		StateDetail:  v.StateDetail,
		Lock:         Lock{Ref: v.Lock.Ref, Binary: v.Lock.Binary, Digest: v.Lock.Digest, State: v.Lock.State},
		Effective: store.CronDeclaration{
			Cron:    v.Effective.Cron,
			TZ:      v.Effective.TZ,
			Overlap: v.Effective.Overlap,
			CatchUp: time.Duration(v.Effective.CatchUpNS),
			Where:   v.Where,
			Args:    emptyToNil(v.Effective.Args),
		},
		OverrideFields: v.Override.Fields,
		OverrideStale:  v.Override.Stale,
		Location:       zoneOrUTC(v.Effective.TZ),
	}
	if len(v.Override.Fields) > 0 {
		row.Override = &store.CronOverride{SetAt: parseRFC3339(v.Override.SetAt)}
	}
	return row
}

// HealthFromView rebuilds the [Health] a renderer reads from the wire shape.
func HealthFromView(v HealthView) Health {
	return Health{
		Timer: crontimer.State{
			Installed: v.Timer.Installed,
			Foreign:   v.Timer.Foreign,
			Enabled:   v.Timer.Enabled,
			Stale:     v.Timer.Stale,
			Path:      v.Timer.Path,
			Binary:    v.Timer.Binary,
			Detail:    v.Timer.Detail,
		},
		LastTick: store.CronTick{
			At:      parseRFC3339(v.LastTick.At),
			Host:    v.LastTick.Host,
			Version: v.LastTick.Version,
			Error:   v.LastTick.Error,
		},
		TickStale:     v.TickStale,
		Schedules:     v.Schedules,
		Armed:         v.Armed,
		Paused:        v.Paused,
		Undeclared:    v.Undeclared,
		Locked:        v.Locked,
		Following:     v.Following,
		Ahead:         v.Ahead,
		MissingBinary: v.MissingBinary,
		StaleOverride: v.StaleOverride,
		Detail:        v.Detail,
		Remedy:        v.Remedy,
	}
}

// FireFromView rebuilds one stored fire from the wire shape.
func FireFromView(v FireView) store.CronFire {
	return store.CronFire{
		ID:         v.ID,
		ScheduleID: v.ScheduleID,
		DueAt:      parseRFC3339(v.DueAt),
		DecidedAt:  parseRFC3339(v.DecidedAt),
		Outcome:    v.Outcome,
		RunID:      v.RunID,
		Detail:     v.Detail,
		Args:       emptyToNil(v.Args),
	}
}

func emptyToNil(args map[string]string) map[string]string {
	if len(args) == 0 {
		return nil
	}
	return args
}

func parseRFC3339(s string) time.Time {
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return at
}

func parseRFC3339Ptr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	at := parseRFC3339(*s)
	if at.IsZero() {
		return nil
	}
	return &at
}

// safety: a zone the reader's tzdata does not carry renders in UTC rather than
// failing the listing, which is the same fallback a locally read row takes.
func zoneOrUTC(tz string) *time.Location {
	loc, err := (&pipelines.ScheduleTrigger{TZ: tz}).Location()
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

// UpcomingAfter returns the next n instants expr matches after now, read in tz.
// It answers for a schedule a reader holds only the cadence of, such as one a
// remote evaluator served.
func UpcomingAfter(expr, tz string, now time.Time, n int) ([]time.Time, error) {
	parsed, err := cronspec.Parse(expr)
	if err != nil {
		return nil, err
	}
	loc, err := (&pipelines.ScheduleTrigger{TZ: tz}).Location()
	if err != nil {
		return nil, err
	}
	return parsed.Upcoming(now, loc, n), nil
}
