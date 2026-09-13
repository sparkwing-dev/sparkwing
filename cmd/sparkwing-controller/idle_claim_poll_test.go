package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestCheckIdleClaimPoll(t *testing.T) {
	cases := []struct {
		name     string
		idle     time.Duration
		hold     time.Duration
		liveness time.Duration
		wantErr  string
	}{
		{name: "the shipped defaults", idle: controller.DefaultMaxIdleClaimPoll, hold: 20 * time.Second, liveness: 30 * time.Second},
		{name: "suggestion off", idle: 0, hold: time.Second, liveness: time.Second},
		{name: "placement off", idle: time.Hour, hold: 0, liveness: 0},
		{
			name: "spread reaches the hold", idle: 15 * time.Second, hold: 18 * time.Second, liveness: 30 * time.Second,
			wantErr: "--placement-hold",
		},
		{
			name: "spread reaches liveness", idle: 12 * time.Second, hold: time.Minute, liveness: 14 * time.Second,
			wantErr: "--placement-liveness",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkIdleClaimPoll(tc.idle, tc.hold, tc.liveness)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkIdleClaimPoll: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v; want one naming %s", err, tc.wantErr)
			}
		})
	}
}

// A runner never waits longer than this however long a controller suggests, so
// the ceiling has to clear the placement windows the shipped chart sets.
func TestLongestHonoredIdlePollClearsTheShippedPlacementWindows(t *testing.T) {
	if got := controller.LongestHonoredIdlePoll(time.Hour); got >= 20*time.Second {
		t.Errorf("a runner may wait %s; the shipped placement hold is 20s", got)
	}
}
