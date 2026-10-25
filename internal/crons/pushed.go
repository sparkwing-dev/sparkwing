package crons

import (
	"context"
	"fmt"
	"time"
)

// ControllerHealth reports schedule counts and the last tick from the store,
// with the timer state naming the controller loop.
func (s *Service) ControllerHealth(ctx context.Context) (Health, error) {
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
	health.Timer.Installed = true
	health.Timer.Enabled = true
	health.Timer.Detail = controllerTimerDetail
	now := s.now()
	// safety: a loop that has never ticked is as unhealthy as one that stopped,
	// which is what the local Health already says of a timer that never fired.
	health.TickStale = tick.At.IsZero() || now.Sub(tick.At) > tickStaleAfter
	health.Detail = health.describeController(now)
	health.Remedy = health.remedy()
	return health, nil
}

// controllerTimerDetail is what a controller's health reports in place of an OS
// timer, because the tick comes from a loop inside the server.
const controllerTimerDetail = "controller loop"

func (h Health) describeController(now time.Time) string {
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
