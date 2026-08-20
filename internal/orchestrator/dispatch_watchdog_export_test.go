package orchestrator

import "context"

func AdmissionWaitActiveForTest(ctx context.Context) bool {
	tracker := admissionWaitTrackerFromContext(ctx)
	participant := admissionWaitParticipantFromContext(ctx)
	if tracker == nil || participant == "" {
		return false
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.active[participant] > 0
}
