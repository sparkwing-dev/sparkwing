package store

import (
	"testing"
	"time"
)

func TestCronTickWindowLeavesRoomForAMinuteTimer(t *testing.T) {
	// safety: a caller waking every minute stamps its tick a little after it
	// started, so a lease window of a full minute would turn the next wake away
	// and skip that minute entirely.
	if cronTickWindow >= time.Minute {
		t.Errorf("CronTickWindow = %s, want a window under a minute", cronTickWindow)
	}
	if cronTickWindow < 30*time.Second {
		t.Errorf("CronTickWindow = %s, want a window near a minute", cronTickWindow)
	}
}
