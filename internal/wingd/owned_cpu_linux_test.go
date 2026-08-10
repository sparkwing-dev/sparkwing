//go:build linux

package wingd

import "testing"

func TestLinuxProcess_SeparatesSelfCPUAndStartIdentity(t *testing.T) {
	process, ok := parseLinuxProcessStat("10 (worker name) S 1 0 0 0 0 0 0 0 0 0 200 300 400 500 0 0 0 0 600")
	if !ok {
		t.Fatal("parse process stat failed")
	}
	if process.startTicks != 600 {
		t.Fatalf("start ticks = %d, want 600", process.startTicks)
	}
	if process.selfCPUSeconds != 5 {
		t.Fatalf("self CPU seconds = %v, want 5", process.selfCPUSeconds)
	}
	if process.cpuSeconds != 14 {
		t.Fatalf("stall CPU seconds = %v, want 14 including reaped children", process.cpuSeconds)
	}
}
