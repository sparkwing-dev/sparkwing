package orchestrator

import (
	"context"
	"sync"
	"time"
)

type admissionWaitTracker struct {
	mu      sync.Mutex
	active  map[string]int
	changed chan struct{}
}

func (t *admissionWaitTracker) begin(participant string) {
	if t == nil || participant == "" {
		return
	}
	t.mu.Lock()
	t.active[participant]++
	t.mu.Unlock()
	t.signal()
}

func (t *admissionWaitTracker) end(participant string) {
	if t == nil || participant == "" {
		return
	}
	t.mu.Lock()
	if t.active[participant] <= 1 {
		delete(t.active, participant)
	} else {
		t.active[participant]--
	}
	t.mu.Unlock()
	t.signal()
}

func (t *admissionWaitTracker) signal() {
	if t == nil {
		return
	}
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

type admissionWaitTrackerKey struct{}

func admissionWaitTrackerFromContext(ctx context.Context) *admissionWaitTracker {
	tracker, _ := ctx.Value(admissionWaitTrackerKey{}).(*admissionWaitTracker)
	return tracker
}

type admissionWaitParticipantKey struct{}

func admissionWaitParticipantFromContext(ctx context.Context) string {
	participant, _ := ctx.Value(admissionWaitParticipantKey{}).(string)
	return participant
}

const DefaultDispatchWaitTimeout = 30 * time.Minute
