package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cache"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

// A cache that verifies grants starts with a finite daily egress cap; a cap
// the operator named, zero included, wins, and a single-team cache keeps
// what it was given.
func TestAMultiTeamCacheStartsWithAFiniteEgressCap(t *testing.T) {
	multi := cache.Config{GrantKey: "grant-key"}
	if got := egressDailyCap(multi, egress.Config{}, egress.Named{}); got != cache.DefaultMultiTeamEgressDailyCapBytes {
		t.Fatalf("multi-team, cap unnamed = %d, want the default", got)
	}
	if got := egressDailyCap(multi, egress.Config{GlobalDailyCapBytes: 0}, egress.Named{DailyCapBytes: true}); got != 0 {
		t.Fatalf("multi-team, cap named 0 = %d, want 0", got)
	}
	if got := egressDailyCap(cache.Config{}, egress.Config{}, egress.Named{}); got != 0 {
		t.Fatalf("single-team, cap unnamed = %d, want off", got)
	}
}
