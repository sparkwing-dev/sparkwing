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
	reg.record(laptop, []string{"location=local"}, &claimCapacity{MaxConcurrent: 2}, nil, now)

	live := reg.live(now, time.Minute, presenceKey{})
	if len(live) != 1 {
		t.Fatalf("live = %+v, want the one recorded runner", live)
	}
	if live[0].TokenPrefix != laptop.tokenPrefix {
		t.Fatalf("live runner carries token prefix %q, want %q", live[0].TokenPrefix, laptop.tokenPrefix)
	}
}

// A registry vouches that no runner is live only after listening for a whole
// liveness window, however many runners of any team have polled it since.
func TestRunnerPresenceCompleteOnlyAfterAWholeWindow(t *testing.T) {
	reg := newRunnerPresenceRegistry()
	start := time.Now()
	if reg.complete(start.Add(time.Hour), time.Minute) {
		t.Fatal("a registry that never accepted a request vouched for its runners")
	}
	reg.listening(start)
	reg.listening(start.Add(time.Second))
	reg.record(presenceKey{tokenPrefix: "swr_other", name: "busy"}, nil, nil, nil, start)
	if reg.complete(start.Add(time.Minute-time.Nanosecond), time.Minute) {
		t.Fatal("a registry younger than the window vouched for its runners")
	}
	if !reg.complete(start.Add(time.Minute), time.Minute) {
		t.Fatal("a registry that listened for the whole window did not vouch for its runners")
	}
	if reg.complete(start.Add(time.Hour), 0) {
		t.Fatal("a controller without a liveness window vouched for its runners")
	}
}
