package wingd

import "testing"

func TestCgroupLimitLines(t *testing.T) {
	if got := cpuMaxLine(2); got != "200000 100000" {
		t.Errorf("cpuMaxLine(2) = %q, want %q", got, "200000 100000")
	}
	if got := cpuMaxLine(0.5); got != "50000 100000" {
		t.Errorf("cpuMaxLine(0.5) = %q, want %q", got, "50000 100000")
	}
	if got := cpuMaxLine(0); got != "max 100000" {
		t.Errorf("cpuMaxLine(0) = %q, want %q", got, "max 100000")
	}
	if got := memMaxLine(8 << 30); got != "8589934592" {
		t.Errorf("memMaxLine = %q, want %q", got, "8589934592")
	}
	if got := memMaxLine(0); got != "max" {
		t.Errorf("memMaxLine(0) = %q, want %q", got, "max")
	}
}
