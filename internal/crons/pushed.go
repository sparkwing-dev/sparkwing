package crons

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ControllerHealth reports schedule counts and the last tick from the store,
// with the timer state naming the active controller loop or local native timer.
func (s *Service) ControllerHealth(ctx context.Context, loopRunning bool) (Health, error) {
	rows, err := s.List(ctx)
	if err != nil {
		return Health{}, err
	}
	health := Health{Schedules: len(rows)}
	for _, r := range rows {
		health.count(r)
	}
	tick, err := s.Operator.GetCronTick(ctx)
	if err != nil {
		return health, err
	}
	health.LastTick = tick
	if s.Side == store.CronWhereController {
		health.Timer.Installed = loopRunning
		health.Timer.Enabled = loopRunning
		health.Timer.Detail = controllerTimerDetail
	} else if s.NativeTimer != nil {
		health.Timer, err = crontimer.Status(ctx, *s.NativeTimer)
		if err != nil {
			if ctx.Err() != nil {
				return health, ctx.Err()
			}
			health.Timer.Detail = err.Error()
			health.Detail = "native schedule timer state is unavailable: " + err.Error()
		}
	} else {
		health.Timer.Detail = "native timer state is unavailable"
		health.Detail = health.Timer.Detail
	}
	now := s.now()
	// safety: a loop that has never ticked is as unhealthy as one that stopped,
	// which is what the local Health already says of a timer that never fired.
	health.TickStale = tick.At.IsZero() || now.Sub(tick.At) > tickStaleAfter
	if health.Detail == "" {
		health.Detail = health.describeController(now)
	}
	health.Remedy = health.remedy()
	return health, nil
}

// controllerTimerDetail is what a controller's health reports in place of an OS
// timer, because the tick comes from a loop inside the server.
const controllerTimerDetail = "controller loop"

func (h Health) describeController(now time.Time) string {
	if h.Timer.Detail != controllerTimerDetail {
		if !h.Timer.Installed {
			return "no native schedule timer is installed; run `sparkwing schedules install` to enable automatic firing"
		}
		if !h.Timer.Enabled || h.Timer.Stale {
			return "the native schedule timer is inactive or stale; run `sparkwing schedules install` to enable automatic firing"
		}
		if h.Armed > 0 && h.TickStale {
			return "the native schedule timer has not ticked within three minutes; inspect its service-manager logs"
		}
		if h.LastTick.Error != "" {
			return "the last tick reported: " + h.LastTick.Error
		}
		return fmt.Sprintf("%d schedule(s) armed and the native timer fires every minute", h.Armed)
	}
	if !h.Timer.Enabled {
		return "the controller schedule evaluator loop is stopped"
	}
	switch {
	case h.Armed == 0 && h.Schedules == 0:
		return "no schedule is pushed here; `sparkwing schedules install --profile <name>` pushes a repository's"
	case h.Armed == 0:
		return fmt.Sprintf("no schedule is armed here (%d paused, %d undeclared)", h.Paused, h.Undeclared)
	case h.LastTick.At.IsZero():
		return fmt.Sprintf("%d schedule(s) are armed and the controller loop has not ticked yet", h.Armed)
	case h.TickStale:
		return fmt.Sprintf("the last tick was %s ago, so the controller loop is not landing; the controller log says why",
			roundDuration(now.Sub(h.LastTick.At)))
	case h.LastTick.Error != "":
		return "the last tick reported: " + h.LastTick.Error
	}
	return fmt.Sprintf("%d schedule(s) armed and the controller loop ticks every minute", h.Armed)
}
