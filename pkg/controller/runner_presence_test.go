package controller

import (
	"testing"
	"time"
)

// The store reads a live runner's team off its credential, so the registry
// has to hand that credential over; a presence without it reads as the
// unauthenticated local runner of the default team and holds another team's
// nodes back.
func TestRunnerPresenceLiveCarriesTheClaimCredential(t *testing.T) {
	reg := newRunnerPresenceRegistry()
	now := time.Now()
	laptop := presenceKey{tokenPrefix: "swr_acmelaptop", name: "laptop"}
	reg.record(laptop, []string{"location=local"}, &claimCapacity{MaxConcurrent: 2}, now)

	live := reg.live(now, time.Minute, presenceKey{})
	if len(live) != 1 {
		t.Fatalf("live = %+v, want the one recorded runner", live)
	}
	if live[0].TokenPrefix != laptop.tokenPrefix {
		t.Fatalf("live runner carries token prefix %q, want %q", live[0].TokenPrefix, laptop.tokenPrefix)
	}
}
