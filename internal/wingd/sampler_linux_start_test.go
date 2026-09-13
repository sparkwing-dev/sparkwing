//go:build linux

package wingd

import (
	"testing"
	"time"
)

func TestLinuxProcessStart_DatesFromTheBootClock(t *testing.T) {
	now := time.Unix(1_000_000, 0)

	if got := linuxProcessStart(now, 3600, 60*uint64(linuxClockTicks)); !got.Equal(now.Add(-3540 * time.Second)) {
		t.Errorf("process start = %v, want %v", got, now.Add(-3540*time.Second))
	}
	if got := linuxProcessStart(now, 3600, 3600*uint64(linuxClockTicks)); !got.Equal(now) {
		t.Errorf("process start = %v, want %v for a process as old as the machine's uptime", got, now)
	}
	for _, tc := range []struct {
		name   string
		uptime float64
		ticks  uint64
	}{
		{"unreadable uptime", 0, 100},
		{"start ticks beyond uptime", 10, 3600 * uint64(linuxClockTicks)},
	} {
		if got := linuxProcessStart(now, tc.uptime, tc.ticks); !got.IsZero() {
			t.Errorf("%s: process start = %v, want undated so the tree goes unmeasured rather than dated wrongly", tc.name, got)
		}
	}
}

func TestLinuxUptime_ReadsTheRunningMachine(t *testing.T) {
	if got := linuxUptime(); got <= 0 {
		t.Fatalf("uptime = %v; want the seconds since boot, because a zero leaves every process undated and every first-seen tree unmeasured", got)
	}
}
