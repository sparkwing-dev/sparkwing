package orchestrator

import "context"

func NodeTimeoutPausedForTest(ctx context.Context) bool {
	controller := nodeTimeoutControllerFromContext(ctx)
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.err == nil && controller.paused
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
