package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cache"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

// A cache that verifies grants starts with a daily egress alarm; an alarm
// the operator named, zero included, wins, and a single-team cache keeps
// what it was given.
func TestAMultiTeamCacheStartsWithAnEgressAlarm(t *testing.T) {
	multi := cache.Config{GrantKey: "grant-key"}
	if got := egressDailyAlarm(multi, egress.Config{}, egress.Named{}); got != cache.DefaultMultiTeamEgressDailyAlarmBytes {
		t.Fatalf("multi-team, alarm unnamed = %d, want the default", got)
	}
	if got := egressDailyAlarm(multi, egress.Config{GlobalDailyAlarmBytes: 0}, egress.Named{DailyAlarmBytes: true}); got != 0 {
		t.Fatalf("multi-team, alarm named 0 = %d, want 0", got)
	}
	if got := egressDailyAlarm(cache.Config{}, egress.Config{}, egress.Named{}); got != 0 {
		t.Fatalf("single-team, alarm unnamed = %d, want off", got)
	}
}
