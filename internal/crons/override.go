package crons

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Override is the edit one host lays over a repository's declared cadence. A
// nil field leaves the declaration in place; a set one replaces it. Args
// replaces the declared arguments whole, so an empty non-nil map runs the
// schedule with none.
type Override struct {
	Cron    *string
	TZ      *string
	Overlap *string
	CatchUp *time.Duration
	Args    map[string]string
}

// Empty reports whether the override sets nothing.
func (o Override) Empty() bool {
	return o.Cron == nil && o.TZ == nil && o.Overlap == nil && o.CatchUp == nil && o.Args == nil
}

// SetOverride lays fields over one schedule's declaration, keeping whatever
// this host had already overridden and had not named again. The declaration
// the result is measured against is refreshed to what the repository says now,
// so setting an override clears any staleness the previous one carried.
//
// The effective cadence is validated the way the config validates a declared
// one, so an override that would stop the schedule evaluating is refused here
// rather than at the next tick.
func (s *Service) SetOverride(ctx context.Context, id string, fields Override) (Row, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return Row{}, err
	}
	if fields.Empty() {
		return Row{}, fmt.Errorf("%s: name at least one of cron, tz, overlap, catch-up or an argument to override",
			DisplayName(sched))
	}
	now := s.now()
	override := store.CronOverride{Base: sched.Declaration(), SetAt: now}
	if sched.Override != nil {
		override = *sched.Override
		override.Base = sched.Declaration()
		override.SetAt = now
	}
	if fields.Cron != nil {
		override.Cron = *fields.Cron
	}
	if fields.TZ != nil {
		override.TZ = *fields.TZ
	}
	if fields.Overlap != nil {
		override.Overlap = *fields.Overlap
	}
	if fields.CatchUp != nil {
		override.CatchUp = fields.CatchUp
	}
	if fields.Args != nil {
		override.Args = fields.Args
	}

	candidate := sched
	candidate.Override = &override
	eval, perr := prepare(candidate)
	if perr != nil {
		return Row{}, fmt.Errorf("%s: this override does not evaluate: %w", DisplayName(sched), perr)
	}
	if err := s.Store.SetCronOverride(ctx, sched.ID, override, now); err != nil {
		return Row{}, err
	}
	if err := s.Store.SetCronScheduleNextDue(ctx, sched.ID, eval.nextAfter(now), now); err != nil {
		return Row{}, err
	}
	return s.reload(ctx, sched.ID)
}

// ClearOverride drops this host's edit, returning the schedule to what the
// repository declares.
func (s *Service) ClearOverride(ctx context.Context, id string) (Row, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return Row{}, err
	}
	now := s.now()
	if err := s.Store.ClearCronOverride(ctx, id, now); err != nil {
		return Row{}, err
	}
	sched.Override = nil
	if eval, perr := prepare(sched); perr == nil {
		if err := s.Store.SetCronScheduleNextDue(ctx, id, eval.nextAfter(now), now); err != nil {
			return Row{}, err
		}
	}
	return s.reload(ctx, id)
}

func (s *Service) reload(ctx context.Context, id string) (Row, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return Row{}, err
	}
	return newRow(ctx, sched, newCheckouts()), nil
}
