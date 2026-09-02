package orchestrator

import (
	"context"
	"time"
)

func NodeTimeoutPausedForTest(ctx context.Context) bool {
	controller := nodeTimeoutControllerFromContext(ctx)
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.err == nil && controller.paused
}

func NodeTimeoutStateForTest(ctx context.Context) (remaining time.Duration, paused, ok bool) {
	controller := nodeTimeoutControllerFromContext(ctx)
	if controller == nil {
		return 0, false, false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.err != nil {
		return 0, controller.paused, false
	}
	if controller.paused {
		return controller.remaining, true, true
	}
	return time.Until(controller.deadline), false, true
}

func SetNodeTimeoutRemainingForTest(ctx context.Context, remaining time.Duration) bool {
	controller := nodeTimeoutControllerFromContext(ctx)
	if controller == nil || remaining <= 0 {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.err != nil || controller.paused {
		return false
	}
	if controller.timer != nil {
		controller.timer.Stop()
	}
	controller.remaining = 0
	controller.deadline = time.Now().Add(remaining)
	controller.armTimerLocked(remaining)
	return true
}

func ForceNodeTimeoutForTest(ctx context.Context) bool {
	controller := nodeTimeoutControllerFromContext(ctx)
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.err != nil || controller.paused {
		return false
	}
	controller.expireLocked()
	return true
}
