package orchestrator

import "context"

func ProgressTimeoutPausedForTest(ctx context.Context) bool {
	controller := progressTimeoutControllerFromContext(ctx)
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.pauseDepth > 0
}

func ExpireProgressTimeoutForTest(ctx context.Context) bool {
	controller := progressTimeoutControllerFromContext(ctx)
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	generation := controller.timerGen
	controller.mu.Unlock()
	controller.finishDeadline(generation)
	return controller.timedOut()
}
