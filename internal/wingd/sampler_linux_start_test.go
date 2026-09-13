//go:build linux

package wingd

import (
	"os"
	"testing"
	"time"
)

func TestLinuxProcessStart_DatesFromTheBootClock(t *testing.T) {
	now := time.Unix(1_000_000, 0)

	if got := linuxProcessStart(now, 3600, 6000); !got.Equal(now.Add(-3540 * time.Second)) {
		t.Errorf("process start = %v, want %v for a process that began 60s after a boot 3600s ago", got, now.Add(-3540*time.Second))
	}
	if got := linuxProcessStart(now, 3600, 360000); !got.Equal(now) {
		t.Errorf("process start = %v, want %v for a process as old as the machine's uptime", got, now)
	}
	for name, tc := range map[string]struct {
		uptime float64
		ticks  uint64
	}{
		"unreadable uptime":         {0, 0},
		"start ticks beyond uptime": {10, 360000},
	} {
		if got := linuxProcessStart(now, tc.uptime, tc.ticks); !got.IsZero() {
			t.Errorf("%s: process start = %v; want undated, so the tree goes unmeasured rather than credited on a bad date", name, got)
		}
	}
}

func TestLinuxUptime_TheRunningMachineExposesAParsableUptime(t *testing.T) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		t.Fatalf("reading this machine's uptime: %v", err)
	}
	if _, ok := parseProcUptime(string(data)); !ok {
		t.Fatalf("this machine's uptime did not parse: %q; every process would go undated and every first-seen tree unmeasured", data)
	}
}
