package crons

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// NewHealthView converts a [Health] for the wire.
func NewHealthView(h Health) controllerwire.HealthView {
	return controllerwire.HealthView{
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

func newTimerStateView(t crontimer.State) controllerwire.TimerStateView {
	return controllerwire.TimerStateView{
		Installed: t.Installed,
		Foreign:   t.Foreign,
		Enabled:   t.Enabled,
		Stale:     t.Stale,
		Path:      t.Path,
		Binary:    t.Binary,
		Detail:    t.Detail,
	}
}

func newTickView(h Health) controllerwire.TickView {
	return controllerwire.TickView{
		At:      RFC3339(h.LastTick.At),
		Host:    h.LastTick.Host,
		Version: h.LastTick.Version,
		Error:   h.LastTick.Error,
	}
}

// NewScheduleView converts a [Row] for the wire.
func NewScheduleView(row Row) controllerwire.ScheduleView {
	override := controllerwire.OverrideView{Fields: row.OverrideFields, Stale: row.OverrideStale}
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
	return controllerwire.ScheduleView{
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
		Lock: controllerwire.LockView{
			Ref:    row.Lock.Ref,
			Binary: row.Lock.Binary,
			Digest: row.Lock.Digest,
			State:  row.Lock.State,
		},
		Override: override,
		Effective: controllerwire.EffectiveView{
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
func NewFireView(fire store.CronFire, runStatus string) controllerwire.FireView {
	return controllerwire.FireView{
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
