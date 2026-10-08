package crons

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// NewHealthView converts a [Health] for the wire.
func NewHealthView(h Health) api.HealthView {
	return api.HealthView{
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

func newTimerStateView(t crontimer.State) api.TimerStateView {
	return api.TimerStateView{
		Installed: t.Installed,
		Foreign:   t.Foreign,
		Enabled:   t.Enabled,
		Stale:     t.Stale,
		Path:      t.Path,
		Binary:    t.Binary,
		Detail:    t.Detail,
	}
}

func newTickView(h Health) api.TickView {
	return api.TickView{
		At:      RFC3339(h.LastTick.At),
		Host:    h.LastTick.Host,
		Version: h.LastTick.Version,
		Error:   h.LastTick.Error,
	}
}

// NewScheduleView converts a [Row] for the wire.
func NewScheduleView(row Row) api.ScheduleView {
	override := api.OverrideView{Fields: row.OverrideFields, Stale: row.OverrideStale}
	if override.Fields == nil {
		override.Fields = []string{}
	}
	if row.Override != nil {
		override.SetAt = RFC3339(row.Override.SetAt)
		override.Cron = row.Override.Cron
		override.TZ = row.Override.TZ
		override.Overlap = row.Override.Overlap
		override.Args = ViewArgs(row.Override.Args)
		if row.Override.CatchUp != nil {
			override.CatchUpNS = int64(*row.Override.CatchUp)
		}
	}
	if override.Args == nil {
		override.Args = map[string]string{}
	}
	return api.ScheduleView{
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
		Lock: api.LockView{
			Ref:    row.Lock.Ref,
			Binary: row.Lock.Binary,
			Digest: row.Lock.Digest,
			State:  row.Lock.State,
		},
		Override: override,
		Effective: api.EffectiveView{
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
func NewFireView(fire store.CronFire, runStatus string) api.FireView {
	return api.FireView{
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
func RowFromView(v api.ScheduleView) Row {
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
		row.Override = &store.CronOverride{
			SetAt:   parseRFC3339(v.Override.SetAt),
			Cron:    v.Override.Cron,
			TZ:      v.Override.TZ,
			Overlap: v.Override.Overlap,
			Args:    overrideArgsFromView(v.Override),
		}
		if v.Override.CatchUpNS != 0 {
			d := time.Duration(v.Override.CatchUpNS)
			row.Override.CatchUp = &d
		}
	}
	// safety: the wire carries the window the evaluator settled on, not the
	// fact that it lowered one, so the marker is the gap between that window
	// and the declaration the view also carries -- an evaluator that does not
	// clamp leaves no gap and draws no marker.
	row.CatchUpClamped = row.Effective.CatchUp != row.CronSchedule.Effective().CatchUp
	return row
}

// safety: an override that replaces the declared arguments with none is a set
// but empty map, which the wire cannot tell from an override that names no
// arguments at all -- the field list can, so it decides.
func overrideArgsFromView(v api.OverrideView) map[string]string {
	for _, field := range v.Fields {
		if field == "args" {
			return ViewArgs(v.Args)
		}
	}
	return nil
}

// HealthFromView rebuilds the [Health] a renderer reads from the wire shape.
func HealthFromView(v api.HealthView) Health {
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
func FireFromView(v api.FireView) store.CronFire {
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
