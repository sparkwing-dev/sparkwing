package crons

import (
	"context"
)

// pause stops a schedule firing. Its cursor still advances on each tick, so
// resuming does not replay the instants that passed while it was paused.
func (s *Service) pause(ctx context.Context, id string) error {
	return s.Schedules.SetCronSchedulePaused(ctx, id, true)
}

// resume lets a paused schedule fire again from its next due instant. The
// cursor moves to now before the pause clears, so a host whose timer was off
// for the whole pause still does not replay it.
func (s *Service) resume(ctx context.Context, id string) error {
	sched, err := s.Schedules.GetCronSchedule(ctx, id)
	if err != nil {
		return err
	}
	now := s.now()
	next := sched.NextDueAt
	if eval, perr := prepare(sched); perr == nil {
		next = eval.nextAfter(now)
	}
	if err := s.Schedules.ResolveCronDue(ctx, id, now, next, nil); err != nil {
		return err
	}
	return s.Schedules.SetCronSchedulePaused(ctx, id, false)
}
